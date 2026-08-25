package controller

import (
	"context"
	"fmt"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/profile"
	"github.com/tlrmchlsmth/oubliette/internal/vcluster"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const Finalizer = "oubliette.tlrmchlsmth.github.io/finalizer"

type OublietteReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	VCluster                vcluster.Manager
	Now                     func() time.Time
	TombstoneRetention      time.Duration
	KueueClusterQueue       string
	KueueManagedLabel       string
	KueueManagedValue       string
	MetricsProfile          profile.MetricsProfile
	MetricsServiceNamespace string
	MetricsServiceName      string
}

func (r *OublietteReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var obj oubv1.Oubliette
	if err := r.Get(ctx, types.NamespacedName{Name: req.Name}, &obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	now := time.Now().UTC()
	if r.Now != nil {
		now = r.Now().UTC()
	}
	namespace, err := namespaceFor(obj.Name)
	if err != nil {
		return r.fail(ctx, &obj, "InvalidName", err)
	}

	if !obj.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &obj, namespace, false, now)
	}
	if conditionTrue(&obj, oubv1.ConditionForgotten) {
		retention := r.TombstoneRetention
		if retention <= 0 {
			retention = time.Hour
		}
		if obj.Status.ForgottenAt != nil {
			remaining := obj.Status.ForgottenAt.Add(retention).Sub(now)
			if remaining <= 0 {
				if err := r.Delete(ctx, &obj); err != nil && !apierrors.IsNotFound(err) {
					return ctrl.Result{}, err
				}
				return ctrl.Result{Requeue: true}, nil
			}
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
		return ctrl.Result{RequeueAfter: retention}, nil
	}
	if !now.Before(obj.Spec.ExpiresAt.Time) {
		return r.finalize(ctx, &obj, namespace, true, now)
	}
	if !controllerutil.ContainsFinalizer(&obj, Finalizer) {
		controllerutil.AddFinalizer(&obj, Finalizer)
		if err := r.Update(ctx, &obj); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}
	p, err := profile.Resolve(obj.Spec.Tier)
	if err != nil {
		return r.fail(ctx, &obj, "UnsupportedTier", err)
	}
	if err := r.ensureBoundary(ctx, &obj, p, namespace); err != nil {
		return r.fail(ctx, &obj, "BoundaryFailed", err)
	}
	if err := r.VCluster.Ensure(ctx, obj.Name, namespace); err != nil {
		return r.fail(ctx, &obj, "ProvisioningFailed", err)
	}
	ready, err := r.VCluster.Ready(ctx, obj.Name, namespace)
	if err != nil {
		return r.fail(ctx, &obj, "ReadinessFailed", err)
	}

	base := obj.DeepCopy()
	obj.Status.ObservedGeneration = obj.Generation
	obj.Status.HostNamespace = namespace
	obj.Status.ProfileGeneration = p.Generation
	obj.Status.VirtualEndpoint = fmt.Sprintf("%s.%s:443", obj.Name, namespace)
	apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionProvisioned, Status: metav1.ConditionTrue, Reason: "ReleaseInstalled", Message: "vCluster release is installed", ObservedGeneration: obj.Generation})
	if ready {
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionReady, Status: metav1.ConditionTrue, Reason: "VirtualAPIReady", Message: "virtual API and bootstrap credential are ready", ObservedGeneration: obj.Generation})
	} else {
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "VirtualAPIStarting", Message: "waiting for the virtual API", ObservedGeneration: obj.Generation})
	}
	metricsReady, err := r.projectMetricsStatus(ctx, &obj, ready)
	if err != nil {
		return r.fail(ctx, &obj, "MetricsReadinessFailed", err)
	}
	if err := r.Status().Patch(ctx, &obj, client.MergeFrom(base)); err != nil {
		return ctrl.Result{}, err
	}
	if ready && metricsReady {
		remaining := obj.Spec.ExpiresAt.Time.Sub(now)
		if r.MetricsProfile.Enabled && remaining > 30*time.Second {
			remaining = 30 * time.Second
		}
		return ctrl.Result{RequeueAfter: remaining}, nil
	}
	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

func (r *OublietteReconciler) finalize(ctx context.Context, obj *oubv1.Oubliette, namespace string, expired bool, now time.Time) (ctrl.Result, error) {
	if expired {
		base := obj.DeepCopy()
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionExpiring, Status: metav1.ConditionTrue, Reason: "TTLExpired", Message: "expiry deadline reached", ObservedGeneration: obj.Generation})
		if err := r.Status().Patch(ctx, obj, client.MergeFrom(base)); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
	if err := r.VCluster.Delete(ctx, obj.Name, namespace); err != nil {
		return ctrl.Result{}, err
	}
	deleted, err := r.deleteNamespace(ctx, namespace)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !deleted {
		return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
	}

	if expired {
		base := obj.DeepCopy()
		forgottenAt := metav1.NewTime(now)
		obj.Status.ForgottenAt = &forgottenAt
		obj.Status.HostNamespace = ""
		obj.Status.VirtualEndpoint = ""
		obj.Status.MetricsEndpoint = ""
		obj.Status.MetricsProfileGeneration = ""
		obj.Status.MetricsIsolationScope = ""
		obj.Status.MetricsTrustDomain = ""
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionReady, Status: metav1.ConditionFalse, Reason: "Forgotten", Message: "all runtime resources have been removed", ObservedGeneration: obj.Generation})
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionFalse, Reason: "Forgotten", Message: "metrics access has been revoked", ObservedGeneration: obj.Generation})
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionForgotten, Status: metav1.ConditionTrue, Reason: "TTLTeardownComplete", Message: "TTL teardown completed", ObservedGeneration: obj.Generation})
		if err := r.Status().Patch(ctx, obj, client.MergeFrom(base)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Hour}, nil
	}
	if controllerutil.ContainsFinalizer(obj, Finalizer) {
		controllerutil.RemoveFinalizer(obj, Finalizer)
		if err := r.Update(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *OublietteReconciler) fail(ctx context.Context, obj *oubv1.Oubliette, reason string, err error) (ctrl.Result, error) {
	base := obj.DeepCopy()
	apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionReady, Status: metav1.ConditionFalse, Reason: reason, Message: err.Error(), ObservedGeneration: obj.Generation})
	_ = r.Status().Patch(ctx, obj, client.MergeFrom(base))
	return ctrl.Result{RequeueAfter: 10 * time.Second}, err
}

func conditionTrue(obj *oubv1.Oubliette, name string) bool {
	c := apiMeta.FindStatusCondition(obj.Status.Conditions, name)
	return c != nil && c.Status == metav1.ConditionTrue
}

func (r *OublietteReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).For(&oubv1.Oubliette{}).Complete(r)
}
