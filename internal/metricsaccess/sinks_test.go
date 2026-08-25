package metricsaccess

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesSecretSinkProjectsResidentCredential(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).Build()
	sink := KubernetesSecretSink{Client: kube, Namespace: "agent", Name: "metrics-access"}
	expiresAt := time.Date(2026, 8, 25, 12, 5, 0, 0, time.UTC)
	if err := sink.DeliverMetricsAccess(t.Context(), "metrics:demo", []byte("opaque-credential"), expiresAt); err != nil {
		t.Fatal(err)
	}
	var secret corev1.Secret
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "agent", Name: "metrics-access"}, &secret); err != nil {
		t.Fatal(err)
	}
	if string(secret.Data["credential"]) != "opaque-credential" || secret.Annotations["oubliette.tlrmchlsmth.github.io/metrics-endpoint"] != "metrics:demo" || secret.Annotations["oubliette.tlrmchlsmth.github.io/expires-at"] != expiresAt.Format(time.RFC3339) {
		t.Fatalf("projected secret = %#v", secret)
	}
}

func TestConnectorSinkDeliversOutsideLifecycleAPI(t *testing.T) {
	called := false
	sink := ConnectorSink(func(_ context.Context, endpoint string, credential []byte, _ time.Time) error {
		called = endpoint == "metrics:demo" && string(credential) == "opaque-credential"
		return nil
	})
	if err := sink.DeliverMetricsAccess(t.Context(), "metrics:demo", []byte("opaque-credential"), time.Now()); err != nil || !called {
		t.Fatalf("connector called = %t, error = %v", called, err)
	}
}
