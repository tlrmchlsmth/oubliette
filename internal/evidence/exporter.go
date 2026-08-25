package evidence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	oubv1 "github.com/tlrmchlsmth/oubliette/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const PendingRunLabel = "evidence.oubliette.tlrmchlsmth.github.io/pending-run"

// PendingRun is the portable handoff from an operator-authoritative benchmark
// collector to the lifecycle controller. It is stored as bundle.json in an
// immutable ConfigMap in the derived namespace so finalization can export it
// before deleting that namespace.
type PendingRun struct {
	Run       Run             `json:"run"`
	Artifacts []InputArtifact `json:"artifacts"`
}

type ConfigMapExporter struct {
	Client client.Client
	Store  Store
}

func (e ConfigMapExporter) ExportBeforeTeardown(ctx context.Context, obj *oubv1.Oubliette, namespace string) error {
	if e.Client == nil || e.Store == nil || obj == nil || namespace == "" {
		return errors.New("evidence exporter is not configured")
	}
	var sources corev1.ConfigMapList
	if err := e.Client.List(ctx, &sources, client.InNamespace(namespace), client.MatchingLabels{PendingRunLabel: "true"}); err != nil {
		return fmt.Errorf("list pending evidence runs: %w", err)
	}
	for index := range sources.Items {
		source := &sources.Items[index]
		if source.Immutable == nil || !*source.Immutable {
			return fmt.Errorf("pending evidence source %s/%s must be immutable", namespace, source.Name)
		}
		encoded, exists := source.BinaryData["bundle.json"]
		if !exists {
			encoded = []byte(source.Data["bundle.json"])
		}
		var pending PendingRun
		if len(encoded) == 0 || json.Unmarshal(encoded, &pending) != nil {
			return fmt.Errorf("pending evidence source %s/%s has invalid bundle.json", namespace, source.Name)
		}
		if pending.Run.Oubliette != obj.Name || pending.Run.OublietteUID != string(obj.UID) {
			return fmt.Errorf("pending evidence source %s/%s does not match Oubliette identity", namespace, source.Name)
		}
		bundle, err := BuildRunBundle(pending.Run, pending.Artifacts)
		if err != nil {
			return fmt.Errorf("build pending evidence source %s/%s: %w", namespace, source.Name, err)
		}
		if _, err := e.Store.Put(ctx, bundle); err != nil {
			return fmt.Errorf("store pending evidence source %s/%s: %w", namespace, source.Name, err)
		}
	}
	return nil
}
