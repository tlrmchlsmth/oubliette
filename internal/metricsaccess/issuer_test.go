package metricsaccess

import (
	"context"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/metricsgateway"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type sinkFunc func(context.Context, string, []byte, time.Time) error

func (f sinkFunc) DeliverMetricsAccess(ctx context.Context, endpoint string, credential []byte, expiresAt time.Time) error {
	return f(ctx, endpoint, credential, expiresAt)
}

func TestIssuerDeliversAudienceBoundAccessToPlacementAdapter(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	codec := metricsgateway.TokenCodec{Key: []byte("0123456789abcdef0123456789abcdef"), Audience: "oubliette-metrics", Now: func() time.Time { return now }}
	obj := readyOubliette()
	issuer := Issuer{Client: fakeClient(t, obj), Codec: codec, Now: func() time.Time { return now }}
	called := false
	err := issuer.IssueTo(context.Background(), Request{Subject: "agent-1", Oubliette: "demo", Placement: PlacementExternal, TTL: time.Minute}, sinkFunc(func(_ context.Context, endpoint string, credential []byte, expiresAt time.Time) error {
		called = true
		if endpoint != "metrics:demo" || !expiresAt.Equal(now.Add(time.Minute)) {
			t.Fatalf("delivery endpoint = %q, expiry = %s", endpoint, expiresAt)
		}
		claims, err := codec.Validate(string(credential))
		if err != nil {
			t.Fatal(err)
		}
		if claims.Subject != "agent-1" || claims.Oubliette != "demo" || claims.TrustDomain != "team-a" || claims.Audience != "oubliette-metrics" {
			t.Fatalf("claims = %#v", claims)
		}
		return nil
	}))
	if err != nil || !called {
		t.Fatalf("IssueTo() called = %t, error = %v", called, err)
	}
}

func TestIssuerRejectsTerminalOubliette(t *testing.T) {
	obj := readyOubliette()
	obj.Status.Conditions = []metav1.Condition{{Type: oubv1.ConditionForgotten, Status: metav1.ConditionTrue}}
	issuer := Issuer{Client: fakeClient(t, obj), Codec: metricsgateway.TokenCodec{Key: []byte("0123456789abcdef0123456789abcdef"), Audience: "oubliette-metrics"}}
	err := issuer.IssueTo(context.Background(), Request{Subject: "agent-1", Oubliette: "demo", Placement: PlacementResident}, sinkFunc(func(context.Context, string, []byte, time.Time) error {
		t.Fatal("terminal access was delivered")
		return nil
	}))
	if err == nil {
		t.Fatalf("terminal IssueTo() error = %v", err)
	}
}

func readyOubliette() *oubv1.Oubliette {
	return &oubv1.Oubliette{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       oubv1.OublietteSpec{ExpiresAt: metav1.NewTime(time.Date(2026, 8, 25, 13, 0, 0, 0, time.UTC))},
		Status: oubv1.OublietteStatus{
			MetricsEndpoint:    "metrics:demo",
			MetricsTrustDomain: "team-a",
			Conditions:         []metav1.Condition{{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionTrue}},
		},
	}
}

func fakeClient(t *testing.T, objects ...*oubv1.Oubliette) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	clientObjects := make([]client.Object, len(objects))
	for index, object := range objects {
		clientObjects[index] = object
	}
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(clientObjects...).Build()
}
