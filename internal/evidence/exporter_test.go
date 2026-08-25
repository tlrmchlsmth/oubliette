package evidence

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConfigMapExporterPersistsPendingRun(t *testing.T) {
	inputs := make([]InputArtifact, 0, len(requiredRunArtifacts))
	for _, name := range requiredRunArtifacts {
		mediaType := "application/json"
		data := validArtifactData(name)
		if strings.HasSuffix(name, ".txt") {
			mediaType, data = "text/plain", []byte("benchmark output\n")
		} else if strings.HasSuffix(name, ".yaml") {
			mediaType = "application/yaml"
		}
		inputs = append(inputs, InputArtifact{Name: name, MediaType: mediaType, Data: data})
	}
	run := validRun()
	run.OublietteUID = "uid-123"
	encoded, err := json.Marshal(PendingRun{Run: run, Artifacts: inputs})
	if err != nil {
		t.Fatal(err)
	}
	immutable := true
	source := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "run-001", Namespace: "oub-demo", Labels: map[string]string{PendingRunLabel: "true"}},
		Immutable:  &immutable,
		BinaryData: map[string][]byte{"bundle.json": encoded},
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(source).Build()
	root := t.TempDir()
	exporter := ConfigMapExporter{Client: kube, Store: DirectoryStore{Root: root}}
	obj := &oubv1.Oubliette{ObjectMeta: metav1.ObjectMeta{Name: "demo", UID: types.UID("uid-123")}}
	if err := exporter.ExportBeforeTeardown(context.Background(), obj, "oub-demo"); err != nil {
		t.Fatal(err)
	}
	digest, err := os.ReadFile(filepath.Join(root, ".run-ids", "run-001"))
	if err != nil || strings.TrimSpace(string(digest)) == "" {
		t.Fatalf("run index=%q error=%v", digest, err)
	}
}
