package controller

import (
	"context"
	"testing"
	"time"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	"github.com/tlrmchlsmth/oubliette/internal/profile"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type fakeVCluster struct {
	ensured, deleted bool
	ready            bool
	beforeDelete     func() error
}

func (f *fakeVCluster) Ensure(context.Context, string, string) error { f.ensured = true; return nil }
func (f *fakeVCluster) Delete(context.Context, string, string) error {
	if f.beforeDelete != nil {
		if err := f.beforeDelete(); err != nil {
			return err
		}
	}
	f.deleted = true
	return nil
}
func (f *fakeVCluster) Ready(context.Context, string, string) (bool, error) { return f.ready, nil }

func TestReconcileReadyAndExpiry(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{oubv1.AddToScheme, corev1.AddToScheme, appsv1.AddToScheme, networkingv1.AddToScheme, discoveryv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "test"}, Spec: oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(now.Add(time.Hour))}}
	clusterQueue := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta2",
		"kind":       "ClusterQueue",
		"metadata":   map[string]any{"name": "test-cq"},
	}}
	ready := true
	metricsService := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "oubliette-metrics", Namespace: "oubliette-system"}}
	metricsEndpoints := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{Name: "oubliette-metrics-1", Namespace: "oubliette-system", Labels: map[string]string{discoveryv1.LabelServiceName: "oubliette-metrics"}},
		Endpoints:  []discoveryv1.Endpoint{{Conditions: discoveryv1.EndpointConditions{Ready: &ready}}},
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).WithObjects(obj, clusterQueue, metricsService, metricsEndpoints).Build()
	vc := &fakeVCluster{ready: true}
	r := &OublietteReconciler{
		Client: c, Scheme: scheme, VCluster: vc, Now: func() time.Time { return now }, TombstoneRetention: time.Minute,
		KueueClusterQueue: "test-cq", KueueManagedLabel: "kueue.example/managed", KueueManagedValue: "true",
		MetricsProfile:          profile.MetricsProfile{Enabled: true, Generation: "metrics-v1", EndpointPrefix: "metrics", IsolationScope: "operator-trust-domain", TrustDomain: "team-a"},
		MetricsServiceNamespace: "oubliette-system", MetricsServiceName: "oubliette-metrics",
	}
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
	if !conditionTrue(&got, oubv1.ConditionMetricsReady) || got.Status.MetricsEndpoint != "metrics:test" || got.Status.MetricsTrustDomain != "team-a" {
		t.Fatalf("metrics not ready: %#v", got.Status)
	}
	var ns corev1.Namespace
	if err := c.Get(context.Background(), types.NamespacedName{Name: "oub-test"}, &ns); err != nil {
		t.Fatal(err)
	}
	if ns.Labels["kueue.example/managed"] != "true" {
		t.Fatalf("Kueue namespace label missing: %#v", ns.Labels)
	}
	localQueue := &unstructured.Unstructured{}
	localQueue.SetGroupVersionKind(localQueueGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Namespace: "oub-test", Name: KueueQueueName}, localQueue); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := unstructured.NestedString(localQueue.Object, "spec", "clusterQueue"); got != "test-cq" {
		t.Fatalf("LocalQueue targets %q, want test-cq", got)
	}
	owner := metav1.GetControllerOf(localQueue)
	if owner == nil || owner.Name != "test" || owner.Kind != "Oubliette" {
		t.Fatalf("LocalQueue controller owner missing: %#v", owner)
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
	if conditionTrue(&got, oubv1.ConditionMetricsReady) || got.Status.MetricsEndpoint != "" {
		t.Fatalf("metrics access survived teardown: %#v", got.Status)
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

type fakeEvidenceExporter struct {
	exported bool
	err      error
}

func (f *fakeEvidenceExporter) ExportBeforeTeardown(context.Context, *oubv1.Oubliette, string) error {
	f.exported = true
	return f.err
}

func TestFinalizeExportsEvidenceBeforeDeletingSources(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{oubv1.AddToScheme, corev1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "measured", Finalizers: []string{Finalizer}}, Spec: oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(now.Add(-time.Minute))}}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "oub-measured"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).WithObjects(obj, ns).Build()
	exporter := &fakeEvidenceExporter{}
	vc := &fakeVCluster{beforeDelete: func() error {
		if !exporter.exported {
			return context.Canceled
		}
		return nil
	}}
	r := &OublietteReconciler{Client: c, Scheme: scheme, VCluster: vc, Now: func() time.Time { return now }, EvidenceExporter: exporter}
	req := ctrl.Request{NamespacedName: types.NamespacedName{Name: obj.Name}}
	if _, err := r.Reconcile(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if !exporter.exported || !vc.deleted {
		t.Fatalf("exported=%v deleted=%v", exporter.exported, vc.deleted)
	}
}

func TestFinalizeBlocksDeletionWhenEvidenceExportFails(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "measured", Finalizers: []string{Finalizer}}, Spec: oubv1.OublietteSpec{Tier: "stub", ExpiresAt: metav1.NewTime(now.Add(-time.Minute))}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&oubv1.Oubliette{}).WithObjects(obj).Build()
	vc := &fakeVCluster{}
	r := &OublietteReconciler{Client: c, Scheme: scheme, VCluster: vc, Now: func() time.Time { return now }, EvidenceExporter: &fakeEvidenceExporter{err: context.DeadlineExceeded}}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: obj.Name}}); err == nil {
		t.Fatal("evidence export failure did not block teardown")
	}
	if vc.deleted {
		t.Fatal("vCluster was deleted after evidence export failure")
	}
}

func TestKueueDisabledUsesStaticCapacity(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "static"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()
	r := &OublietteReconciler{Client: c, Scheme: scheme}
	if err := r.ensureKueue(context.Background(), obj, "oub-static"); err != nil {
		t.Fatal(err)
	}
}

func TestKueueRequiresConfiguredClusterQueue(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := oubv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "queued"}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(obj).Build()
	r := &OublietteReconciler{Client: c, Scheme: scheme, KueueClusterQueue: "missing"}
	if err := r.ensureKueue(context.Background(), obj, "oub-queued"); err == nil {
		t.Fatal("missing configured ClusterQueue was accepted")
	}
}

func TestNamespaceLength(t *testing.T) {
	if _, err := namespaceFor("abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefgh"); err == nil {
		t.Fatal("long name accepted")
	}
}
