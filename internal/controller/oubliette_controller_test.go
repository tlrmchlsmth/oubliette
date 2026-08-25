package controller

import (
	"context"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeVCluster struct {
	ensured, deleted bool
	ready            bool
}

func (f *fakeVCluster) Ensure(context.Context, string, string) error        { f.ensured = true; return nil }
func (f *fakeVCluster) Delete(context.Context, string, string) error        { f.deleted = true; return nil }
func (f *fakeVCluster) Ready(context.Context, string, string) (bool, error) { return f.ready, nil }

func TestReconcileReadyAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{oubv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(now.Add(time.Hour))}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).WithObjects(obj).Build()
	vc := &fakeVCluster{ready: true}
	r := &OublietteReconciler{Client: c, Scheme: scheme, VCluster: vc, Now: func() time.Time { return now }, TombstoneRetention: time.Minute}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: "test"}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	var got oubv1.Oubliette
	if err := c.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(&got, oubv1.ConditionReady) || !vc.ensured {
		t.Fatalf("not ready: %#v", got.Status)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: "oub-test"}, &ns); err != nil {
		t.Fatal(err)
	}

	now = now.Add(2 * time.Hour)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), req.NamespacedName, &got); err != nil {
		t.Fatal(err)
	}
	if !conditionTrue(&got, oubv1.ConditionForgotten) || !vc.deleted {
		t.Fatalf("not forgotten: %#v", got.Status)
	}
	now = now.Add(2 * time.Minute)
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), req.NamespacedName, &got); err == nil {
		t.Fatal("expired tombstone was not garbage-collected")
	}
}

func TestNamespaceLength(t *testing.T) {
	if _, err := namespaceFor("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefgh"); err == nil {
		t.Fatal("long name accepted")
	}
}
