package controller

import (
	"context"
	"errors"
	"fmt"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *OublietteReconciler) projectMetricsStatus(ctx context.Context, obj *oubv1.Oubliette, virtualReady bool) (bool, error) {
	profile := r.MetricsProfile
	if !profile.Enabled {
		obj.Status.MetricsEndpoint = ""
		obj.Status.MetricsProfileGeneration = ""
		obj.Status.MetricsIsolationScope = ""
		obj.Status.MetricsTrustDomain = ""
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionFalse, Reason: "MetricsDisabled", Message: "metrics access is not enabled by the resolved profile", ObservedGeneration: obj.Generation})
		return true, nil
	}
	if profile.Generation == "" || profile.EndpointPrefix == "" || profile.IsolationScope == "" || profile.TrustDomain == "" || r.MetricsServiceNamespace == "" || r.MetricsServiceName == "" {
		return false, errors.New("enabled metrics profile is incomplete")
	}
	obj.Status.MetricsEndpoint = fmt.Sprintf("%s:%s", profile.EndpointPrefix, obj.Name)
	obj.Status.MetricsProfileGeneration = profile.Generation
	obj.Status.MetricsIsolationScope = profile.IsolationScope
	obj.Status.MetricsTrustDomain = profile.TrustDomain
	if !virtualReady {
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionFalse, Reason: "VirtualAPINotReady", Message: "metrics access waits for virtual API readiness", ObservedGeneration: obj.Generation})
		return false, nil
	}
	ready, err := r.metricsServiceReady(ctx)
	if err != nil {
		return false, err
	}
	if !ready {
		apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionFalse, Reason: "QueryGatewayNotReady", Message: "waiting for the private metrics query gateway", ObservedGeneration: obj.Generation})
		return false, nil
	}
	apiMeta.SetStatusCondition(&obj.Status.Conditions, metav1.Condition{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionTrue, Reason: "QueryGatewayReady", Message: "private scoped metrics access is ready", ObservedGeneration: obj.Generation})
	return true, nil
}

func (r *OublietteReconciler) metricsServiceReady(ctx context.Context) (bool, error) {
	var service corev1.Service
	key := types.NamespacedName{Namespace: r.MetricsServiceNamespace, Name: r.MetricsServiceName}
	if err := r.Get(ctx, key, &service); err != nil {
		if client.IgnoreNotFound(err) == nil {
			return false, nil
		}
		return false, err
	}
	var slices discoveryv1.EndpointSliceList
	if err := r.List(ctx, &slices, client.InNamespace(key.Namespace), client.MatchingLabels{discoveryv1.LabelServiceName: key.Name}); err != nil {
		return false, err
	}
	for _, slice := range slices.Items {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready != nil && *endpoint.Conditions.Ready {
				return true, nil
			}
		}
	}
	return false, nil
}
