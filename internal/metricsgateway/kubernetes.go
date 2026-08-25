package metricsgateway

import (
	"context"
	"errors"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KubernetesResolver struct {
	Client                client.Client
	Codec                 TokenCodec
	Upstream              string
	UpstreamAuthorization string
	ProfileGeneration     string
	Policy                Policy
	Now                   func() time.Time
}

func (r KubernetesResolver) Resolve(ctx context.Context, authorization string) (Scope, error) {
	resolver := TokenResolver{
		Codec: r.Codec,
		ResolveClaims: func(ctx context.Context, claims Claims) (Scope, error) {
			if r.Client == nil || r.ProfileGeneration == "" {
				return Scope{}, ErrInvalidCredential
			}
			var obj oubv1.Oubliette
			if err := r.Client.Get(ctx, client.ObjectKey{Name: claims.Oubliette}, &obj); err != nil {
				return Scope{}, ErrInvalidCredential
			}
			if !obj.DeletionTimestamp.IsZero() || !r.now().Before(obj.Spec.ExpiresAt.Time) || apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionForgotten) ||
				!apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionMetricsReady) {
				return Scope{}, ErrInvalidCredential
			}
			if obj.Status.MetricsEndpoint == "" || obj.Status.MetricsProfileGeneration != r.ProfileGeneration ||
				obj.Status.MetricsTrustDomain != claims.TrustDomain {
				return Scope{}, ErrInvalidCredential
			}
			return Scope{
				Oubliette:             obj.Name,
				TrustDomain:           obj.Status.MetricsTrustDomain,
				Upstream:              r.Upstream,
				UpstreamAuthorization: r.UpstreamAuthorization,
				Policy:                r.Policy,
			}, nil
		},
	}
	scope, err := resolver.Resolve(ctx, authorization)
	if err != nil {
		return Scope{}, errors.Join(ErrInvalidCredential, err)
	}
	return scope, nil
}

func (r KubernetesResolver) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}
