package metricsgateway

import (
	"context"
	"errors"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTokenCodec(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	codec := TokenCodec{Key: []byte("0123456789abcdef0123456789abcdef"), Audience: "oubliette-metrics", Now: func() time.Time { return now }}
	want := Claims{Subject: "agent-1", Oubliette: "demo", TrustDomain: "team-a", Audience: "oubliette-metrics", IssuedUnix: now.Unix(), ExpiresUnix: now.Add(time.Minute).Unix()}
	token, err := codec.Issue(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Validate(token)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("claims = %#v, want %#v", got, want)
	}
	if _, err := codec.Validate(token + "x"); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("tampered token error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := codec.Validate(token); !errors.Is(err, ErrExpiredCredential) {
		t.Fatalf("expired token error = %v", err)
	}
}

func TestKubernetesResolverRevokesTerminalAccess(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	codec := TokenCodec{Key: []byte("0123456789abcdef0123456789abcdef"), Audience: "oubliette-metrics", Now: func() time.Time { return now }}
	token, err := codec.Issue(Claims{Subject: "agent-1", Oubliette: "demo", TrustDomain: "team-a", Audience: "oubliette-metrics", ExpiresUnix: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &oubv1.Oubliette{
		ObjectMeta: metav1.ObjectMeta{Name: "demo"},
		Spec:       oubv1.OublietteSpec{ExpiresAt: metav1.NewTime(now.Add(time.Hour))},
		Status: oubv1.OublietteStatus{
			MetricsEndpoint:          "metrics:demo",
			MetricsProfileGeneration: "metrics-v1",
			MetricsTrustDomain:       "team-a",
			Conditions:               []metav1.Condition{{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionTrue}},
		},
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).WithObjects(obj).Build()
	resolver := KubernetesResolver{Client: kube, Codec: codec, Upstream: "https://prometheus.example", ProfileGeneration: "metrics-v1", Policy: testPolicy(), Now: func() time.Time { return now }}
	if _, err := resolver.Resolve(context.Background(), "Bearer "+token); err != nil {
		t.Fatalf("ready credential rejected: %v", err)
	}
	obj.Status.Conditions = []metav1.Condition{{Type: oubv1.ConditionMetricsReady, Status: metav1.ConditionFalse}, {Type: oubv1.ConditionForgotten, Status: metav1.ConditionTrue}}
	if err := kube.Status().Update(context.Background(), obj); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "Bearer "+token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("terminal credential error = %v", err)
	}
}

func TestTokenResolverBindsTrustedScope(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	codec := TokenCodec{Key: []byte("0123456789abcdef0123456789abcdef"), Audience: "oubliette-metrics", Now: func() time.Time { return now }}
	token, err := codec.Issue(Claims{Subject: "agent-1", Oubliette: "demo", TrustDomain: "team-a", Audience: "oubliette-metrics", ExpiresUnix: now.Add(time.Minute).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	resolver := TokenResolver{
		Codec: codec,
		ResolveClaims: func(context.Context, Claims) (Scope, error) {
			return Scope{Oubliette: "other", TrustDomain: "team-a"}, nil
		},
	}
	if _, err := resolver.Resolve(context.Background(), "Bearer "+token); !errors.Is(err, ErrInvalidCredential) {
		t.Fatalf("scope mismatch error = %v", err)
	}
}
