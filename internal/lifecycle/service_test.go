package lifecycle

import (
	"context"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateAndRenew(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s := &Service{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).Build(), Now: func() time.Time { return now }}
	got, err := s.Create(context.Background(), CreateInput{Name: "demo", Tier: "stub", TTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Minute).Format(time.RFC3339); got.ExpiresAt != want {
		t.Fatalf("expires = %s, want %s", got.ExpiresAt, want)
	}
	now = now.Add(30 * time.Second)
	renewed, err := s.Renew(context.Background(), RenewInput{Name: "demo", TTLSeconds: 120})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(2 * time.Minute).Format(time.RFC3339); renewed.ExpiresAt != want {
		t.Fatalf("renewed = %s, want %s", renewed.ExpiresAt, want)
	}
}

func TestValidation(t *testing.T) {
	for _, tc := range []CreateInput{{Name: "Upper", TTLSeconds: 60}, {Name: "ok", TTLSeconds: 1}, {Name: "ok", Tier: "gpu", TTLSeconds: 60}} {
		scheme := runtime.NewScheme()
		_ = oubv1.AddToScheme(scheme)
		s := &Service{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
		if _, err := s.Create(context.Background(), tc); err == nil {
			t.Fatalf("Create(%+v) unexpectedly succeeded", tc)
		}
	}
}
