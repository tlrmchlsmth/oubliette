package vcluster

import (
	"bytes"
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestValuesPinNumericNonRootIdentity(t *testing.T) {
	controlPlane := values("test", "oub-test")["controlPlane"].(map[string]any)
	statefulSet := controlPlane["statefulSet"].(map[string]any)
	security := statefulSet["security"].(map[string]any)
	for _, contextName := range []string{"podSecurityContext", "containerSecurityContext"} {
		context := security[contextName].(map[string]any)
		if context["runAsNonRoot"] != true || context["runAsUser"] != int64(1000) || context["runAsGroup"] != int64(1000) {
			t.Fatalf("%s does not pin the vCluster image's numeric non-root identity: %#v", contextName, context)
		}
	}
}

func TestEnsureHandoffCreatesAndRepairsVirtualIdentity(t *testing.T) {
	ctx := context.Background()
	host := fake.NewClientBuilder().WithScheme(handoffScheme(t)).WithObjects(bootstrapSecret("config-bytes")).Build()
	virtual := fake.NewClientBuilder().WithScheme(handoffScheme(t)).Build()
	manager := &HelmManager{
		Client: host,
		VirtualClientFactory: func(config []byte) (client.Client, error) {
			if !bytes.Equal(config, []byte("config-bytes")) {
				t.Fatalf("bootstrap config = %q", config)
			}
			return virtual, nil
		},
	}

	if err := manager.ensureHandoff(ctx, "test", "oub-test"); err != nil {
		t.Fatal(err)
	}
	if ready, err := handoffReady(ctx, virtual); err != nil || !ready {
		t.Fatalf("handoffReady() = %v, %v; want true, nil", ready, err)
	}

	var serviceAccount corev1.ServiceAccount
	if err := virtual.Get(ctx, types.NamespacedName{Name: HandoffServiceAccount, Namespace: HandoffNamespace}, &serviceAccount); err != nil {
		t.Fatal(err)
	}
	if err := virtual.Delete(ctx, &serviceAccount); err != nil {
		t.Fatal(err)
	}
	var binding rbacv1.ClusterRoleBinding
	if err := virtual.Get(ctx, types.NamespacedName{Name: HandoffClusterRoleBinding}, &binding); err != nil {
		t.Fatal(err)
	}
	binding.Subjects = []rbacv1.Subject{{Kind: rbacv1.UserKind, Name: "wrong"}}
	if err := virtual.Update(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureHandoff(ctx, "test", "oub-test"); err != nil {
		t.Fatal(err)
	}
	if ready, err := handoffReady(ctx, virtual); err != nil || !ready {
		t.Fatalf("repaired handoffReady() = %v, %v; want true, nil", ready, err)
	}

	if err := virtual.Get(ctx, types.NamespacedName{Name: HandoffClusterRoleBinding}, &binding); err != nil {
		t.Fatal(err)
	}
	binding.RoleRef.Name = "view"
	if err := virtual.Update(ctx, &binding); err != nil {
		t.Fatal(err)
	}
	if err := manager.ensureHandoff(ctx, "test", "oub-test"); err != nil {
		t.Fatal(err)
	}
	if ready, err := handoffReady(ctx, virtual); err != nil || ready {
		t.Fatalf("handoff after immutable drift replacement = %v, %v; want false, nil", ready, err)
	}
	if err := manager.ensureHandoff(ctx, "test", "oub-test"); err != nil {
		t.Fatal(err)
	}
	if ready, err := handoffReady(ctx, virtual); err != nil || !ready {
		t.Fatalf("recreated handoffReady() = %v, %v; want true, nil", ready, err)
	}
}

func TestReadyRequiresControlPlaneBootstrapAndExactHandoff(t *testing.T) {
	ctx := context.Background()
	virtual := fake.NewClientBuilder().WithScheme(handoffScheme(t)).Build()
	factory := func([]byte) (client.Client, error) { return virtual, nil }

	tests := []struct {
		name    string
		objects []client.Object
		want    bool
	}{
		{name: "missing bootstrap", objects: []client.Object{readyDeployment()}},
		{name: "empty bootstrap", objects: []client.Object{bootstrapSecret(""), readyDeployment()}},
		{name: "control plane unavailable", objects: []client.Object{bootstrapSecret("config-bytes")}},
		{name: "handoff missing", objects: []client.Object{bootstrapSecret("config-bytes"), readyDeployment()}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host := fake.NewClientBuilder().WithScheme(handoffScheme(t)).WithObjects(tt.objects...).Build()
			manager := &HelmManager{Client: host, VirtualClientFactory: factory}
			ready, err := manager.Ready(ctx, "test", "oub-test")
			if err != nil || ready != tt.want {
				t.Fatalf("Ready() = %v, %v; want %v, nil", ready, err, tt.want)
			}
		})
	}

	host := fake.NewClientBuilder().WithScheme(handoffScheme(t)).WithObjects(bootstrapSecret("config-bytes"), readyDeployment()).Build()
	manager := &HelmManager{Client: host, VirtualClientFactory: factory}
	if err := manager.ensureHandoff(ctx, "test", "oub-test"); err != nil {
		t.Fatal(err)
	}
	ready, err := manager.Ready(ctx, "test", "oub-test")
	if err != nil || !ready {
		t.Fatalf("Ready() = %v, %v; want true, nil", ready, err)
	}
}

func TestEnsureHandoffReturnsVirtualAPIFailuresForRetry(t *testing.T) {
	host := fake.NewClientBuilder().WithScheme(handoffScheme(t)).WithObjects(bootstrapSecret("config-bytes")).Build()
	manager := &HelmManager{
		Client: host,
		VirtualClientFactory: func([]byte) (client.Client, error) {
			return nil, errors.New("virtual API unavailable")
		},
	}
	if err := manager.ensureHandoff(context.Background(), "test", "oub-test"); err == nil {
		t.Fatal("virtual API failure was ignored")
	}
}

func TestReadyRejectsMalformedBootstrapConfig(t *testing.T) {
	host := fake.NewClientBuilder().WithScheme(handoffScheme(t)).WithObjects(bootstrapSecret("not-a-kubeconfig"), readyDeployment()).Build()
	manager := &HelmManager{Client: host}
	if ready, err := manager.Ready(context.Background(), "test", "oub-test"); err == nil || ready {
		t.Fatalf("Ready() = %v, %v; want false and an error", ready, err)
	}
}

func handoffScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, appsv1.AddToScheme, rbacv1.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func bootstrapSecret(config string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vc-test", Namespace: "oub-test"},
		Data:       map[string][]byte{"config": []byte(config)},
	}
}

func readyDeployment() *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "oub-test"},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 1},
	}
}
