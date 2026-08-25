package metricsaccess

import (
	"context"
	"errors"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/metricsgateway"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type Placement string

const (
	PlacementResident Placement = "resident"
	PlacementExternal Placement = "external"
)

type Request struct {
	Subject   string
	Oubliette string
	Placement Placement
	TTL       time.Duration
}

// DeliverySink is implemented by a trusted placement adapter. A resident
// adapter projects the bytes into the agent workload; an external adapter
// sends them over its authenticated consumer-owned connector. Implementations
// must not place credential bytes in MCP results, logs, or model transcripts.
type DeliverySink interface {
	DeliverMetricsAccess(ctx context.Context, endpointIdentity string, credential []byte, expiresAt time.Time) error
}

type Issuer struct {
	Client client.Client
	Codec  metricsgateway.TokenCodec
	Now    func() time.Time
}

func (i Issuer) IssueTo(ctx context.Context, request Request, sink DeliverySink) error {
	if i.Client == nil || sink == nil || request.Subject == "" || request.Oubliette == "" || (request.Placement != PlacementResident && request.Placement != PlacementExternal) {
		return errors.New("metrics access request is incomplete")
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	var obj oubv1.Oubliette
	if err := i.Client.Get(ctx, client.ObjectKey{Name: request.Oubliette}, &obj); err != nil {
		return errors.New("metrics access is unavailable")
	}
	now := i.now()
	if !obj.DeletionTimestamp.IsZero() || !now.Before(obj.Spec.ExpiresAt.Time) || apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionForgotten) || !apiMeta.IsStatusConditionTrue(obj.Status.Conditions, oubv1.ConditionMetricsReady) || obj.Status.MetricsEndpoint == "" || obj.Status.MetricsTrustDomain == "" {
		return errors.New("metrics access is unavailable")
	}
	expiresAt := now.Add(ttl)
	token, err := i.Codec.Issue(metricsgateway.Claims{
		Subject:     request.Subject,
		Oubliette:   obj.Name,
		TrustDomain: obj.Status.MetricsTrustDomain,
		Audience:    i.Codec.Audience,
		IssuedUnix:  now.Unix(),
		ExpiresUnix: expiresAt.Unix(),
	})
	if err != nil {
		return err
	}
	return sink.DeliverMetricsAccess(ctx, obj.Status.MetricsEndpoint, []byte(token), expiresAt)
}

func (i Issuer) now() time.Time {
	if i.Now != nil {
		return i.Now().UTC()
	}
	return time.Now().UTC()
}
