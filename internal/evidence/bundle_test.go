package evidence

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func validRun() Run {
	start := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	return Run{
		RunID:              "run-001",
		Oubliette:          "demo",
		OublietteUID:       "uid-123",
		ProfileGenerations: map[string]string{"resource": "gpu-v1", "metrics": "metrics-v1"},
		IsolationScope:     "operator-trust-domain",
		TrustDomain:        "team-a",
		WarmupStart:        start,
		MeasurementStart:   start.Add(time.Minute),
		MeasurementEnd:     start.Add(2 * time.Minute),
		ExportedAt:         start.Add(3 * time.Minute),
		Outcome:            "valid",
	}
}

func TestBuildIsDeterministicAndStoresArtifacts(t *testing.T) {
	inputs := []InputArtifact{
		{Name: "results/result.json", MediaType: "application/json", Data: []byte("{\"requests\":2}\n")},
		{Name: "manifests/rendered.yaml", MediaType: "application/yaml", Data: []byte("kind: Pod\n")},
	}
	first, err := Build(validRun(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Build(validRun(), []InputArtifact{inputs[1], inputs[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.BundleDigest != second.Manifest.BundleDigest || !bytes.Equal(first.ManifestBytes, second.ManifestBytes) {
		t.Fatal("bundle depends on input artifact order")
	}
	root := t.TempDir()
	location, err := (DirectoryStore{Root: root}).Put(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(location, "results", "result.json")); err != nil {
		t.Fatal(err)
	}
	if again, err := (DirectoryStore{Root: root}).Put(context.Background(), first); err != nil || again != location {
		t.Fatalf("idempotent Put = %q, %v", again, err)
	}
}

func TestBuildRejectsSecretsAndInvalidRuns(t *testing.T) {
	for _, artifact := range []InputArtifact{
		{Name: "credential.yaml", MediaType: "text/yaml", Data: []byte("token: secret-value\n")},
		{Name: "credential.json", MediaType: "application/json", Data: []byte(`{"client_secret":"secret-value"}`)},
	} {
		if _, err := Build(validRun(), []InputArtifact{artifact}); err == nil {
			t.Fatalf("secret-bearing artifact %q unexpectedly accepted", artifact.Name)
		}
	}
	run := validRun()
	run.Outcome = "invalid"
	if _, err := Build(run, nil); err == nil {
		t.Fatal("invalid run without reason unexpectedly accepted")
	}
	run.InvalidationReason = "duplicate scrape target"
	if _, err := Build(run, nil); err != nil {
		t.Fatalf("invalid run with reason rejected: %v", err)
	}
}

func TestBuildRunBundleRequiresPortableProvenance(t *testing.T) {
	if _, err := BuildRunBundle(validRun(), []InputArtifact{{Name: "benchmark/results.json", MediaType: "application/json", Data: []byte(`{}`)}}); err == nil || !strings.Contains(err.Error(), "source/spec.json") {
		t.Fatalf("incomplete run bundle error = %v", err)
	}
	inputs := make([]InputArtifact, 0, len(requiredRunArtifacts))
	for _, name := range requiredRunArtifacts {
		mediaType := "application/json"
		if strings.HasSuffix(name, ".txt") {
			mediaType = "text/plain"
		} else if strings.HasSuffix(name, ".yaml") {
			mediaType = "application/yaml"
		}
		inputs = append(inputs, InputArtifact{Name: name, MediaType: mediaType, Data: []byte(`{}`)})
	}
	if _, err := BuildRunBundle(validRun(), inputs); err != nil {
		t.Fatal(err)
	}
}

func TestDirectoryStoreDoesNotOverwrite(t *testing.T) {
	bundle, err := Build(validRun(), nil)
	if err != nil {
		t.Fatal(err)
	}
	store := DirectoryStore{Root: t.TempDir()}
	if _, err := store.Put(context.Background(), bundle); err != nil {
		t.Fatal(err)
	}
	bundle.ManifestBytes = append([]byte(nil), bundle.ManifestBytes...)
	bundle.ManifestBytes[0] = 'X'
	if _, err := store.Put(context.Background(), bundle); err == nil {
		t.Fatal("store overwrote an existing digest")
	}
}
