package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestCreateAndRenew(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	s := &Service{Client: fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).Build(), Now: func() time.Time { return now }}
	ctx := WithCaller(context.Background(), "consumer-a")
	got, err := s.Create(ctx, CreateInput{Name: "demo", Tier: "stub", TTLSeconds: 60})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(time.Minute).Format(time.RFC3339); got.ExpiresAt != want {
		t.Fatalf("expires = %s, want %s", got.ExpiresAt, want)
	}
	now = now.Add(30 * time.Second)
	renewed, err := s.Renew(ctx, RenewInput{Name: "demo", TTLSeconds: 120})
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
		if _, err := s.Create(WithCaller(context.Background(), "consumer-a"), tc); err == nil {
			t.Fatalf("Create(%+v) unexpectedly succeeded", tc)
		}
	}
}

func TestCallerScopedLifecycle(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	kube := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).Build()
	s := &Service{Client: kube, Now: func() time.Time { return now }}
	callerA := WithCaller(context.Background(), "consumer-a")
	callerB := WithCaller(context.Background(), "consumer-b")

	if _, err := s.Create(callerA, CreateInput{Name: "owned-a", Tier: "stub", TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(callerB, CreateInput{Name: "owned-b", Tier: "stub", TTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(callerA, CreateInput{Name: "owned-a", Tier: "stub", TTLSeconds: 60}); err != nil {
		t.Fatalf("same-caller idempotent create failed: %v", err)
	}
	if _, err := s.Create(callerB, CreateInput{Name: "owned-a", Tier: "stub", TTLSeconds: 60}); !apierrors.IsAlreadyExists(err) {
		t.Fatalf("cross-caller create error = %v, want AlreadyExists", err)
	}

	listed, err := s.List(callerA, ListInput{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Name != "owned-a" {
		t.Fatalf("caller A list = %#v", listed.Items)
	}

	if _, err := s.Get(callerB, NameInput{Name: "owned-a"}); !apierrors.IsNotFound(err) {
		t.Fatalf("cross-caller get error = %v, want NotFound", err)
	}
	if _, err := s.Renew(callerB, RenewInput{Name: "owned-a", TTLSeconds: 120}); !apierrors.IsNotFound(err) {
		t.Fatalf("cross-caller renew error = %v, want NotFound", err)
	}
	if _, err := s.Delete(callerB, NameInput{Name: "owned-a"}); !apierrors.IsNotFound(err) {
		t.Fatalf("cross-caller delete error = %v, want NotFound", err)
	}
	var stillOwned oubv1.Oubliette
	if err := kube.Get(context.Background(), client.ObjectKey{Name: "owned-a"}, &stillOwned); err != nil {
		t.Fatalf("cross-caller delete mutated object: %v", err)
	}
}

func TestUnownedAndUnauthenticatedResourcesFailClosed(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	unowned := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "legacy"}, Spec: oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(time.Now().Add(time.Hour))}}
	s := &Service{Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(unowned).Build()}

	if _, err := s.Get(WithCaller(context.Background(), "consumer-a"), NameInput{Name: "legacy"}); !apierrors.IsNotFound(err) {
		t.Fatalf("unowned Get() error = %v, want NotFound", err)
	}
	if _, err := s.List(WithCaller(context.Background(), "consumer-a"), ListInput{}); err != nil {
		t.Fatalf("unowned List() error = %v", err)
	}
	if _, err := s.Get(context.Background(), NameInput{Name: "legacy"}); err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("unauthenticated Get() error = %v", err)
	}
}
