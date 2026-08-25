package profile

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestResolve(t *testing.T) {
	p, err := Resolve("stub")
	if err != nil {
		t.Fatal(err)
	}
	if p.Generation != StubGeneration {
		t.Fatalf("generation = %q", p.Generation)
	}
	if got := p.Quota[corev1.ResourcePersistentVolumeClaims]; !got.IsZero() {
		t.Fatalf("pvc quota = %s", got.String())
	}
	if _, err := Resolve("gpu"); err == nil {
		t.Fatal("gpu unexpectedly resolved")
	}
}
