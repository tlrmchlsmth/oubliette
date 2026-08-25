package controller

import (
	"context"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/profile"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

const (
	EnforcedLabel  = "oubliette.tlrmchlsmth.github.io/enforced"
	IDLabel        = "oubliette.tlrmchlsmth.github.io/id"
	ManagedByLabel = "vcluster.loft.sh/managed-by"
)

func (r *OublietteReconciler) ensureBoundary(ctx context.Context, obj *oubv1.Oubliette, p profile.Profile, namespace string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = map[string]string{}
		}
		ns.Labels[EnforcedLabel] = "true"
		ns.Labels[IDLabel] = obj.Name
		return controllerutil.SetControllerReference(obj, ns, r.Scheme)
	}); err != nil {
		return err
	}

	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "stub", Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, quota, func() error {
		quota.Spec.Hard = p.Quota.DeepCopy()
		return controllerutil.SetControllerReference(obj, quota, r.Scheme)
	}); err != nil {
		return err
	}

	selector := metav1.LabelSelector{MatchLabels: map[string]string{ManagedByLabel: obj.Name}}
	controlPlaneSelector := metav1.LabelSelector{MatchLabels: map[string]string{"app": "vcluster", "release": obj.Name}}
	deny := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "workload-default-deny", Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, deny, func() error {
		deny.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
		}
		return controllerutil.SetControllerReference(obj, deny, r.Scheme)
	}); err != nil {
		return err
	}

	intra := &networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "workload-intra-oubliette", Namespace: namespace}}
	if _, err := controllerutil.CreateOrUpdate(ctx, r.Client, intra, func() error {
		intra.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: selector,
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{PodSelector: &selector}}}},
			Egress:      []networkingv1.NetworkPolicyEgressRule{{To: []networkingv1.NetworkPolicyPeer{{PodSelector: &selector}, {PodSelector: &controlPlaneSelector}}}},
		}
		return controllerutil.SetControllerReference(obj, intra, r.Scheme)
	}); err != nil {
		return err
	}
	return nil
}

func (r *OublietteReconciler) deleteNamespace(ctx context.Context, namespace string) (bool, error) {
	var ns corev1.Namespace
	err := r.Get(ctx, types.NamespacedName{Name: namespace}, &ns)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if ns.DeletionTimestamp.IsZero() {
		if err := r.Delete(ctx, &ns); err != nil && !apierrors.IsNotFound(err) {
			return false, err
		}
	}
	return false, nil
}

func namespaceFor(name string) (string, error) {
	namespace := "oub-" + name
	if len(namespace) > 63 {
		return "", apierrors.NewBadRequest("Oubliette name must be at most 59 characters")
	}
	return namespace, nil
}
