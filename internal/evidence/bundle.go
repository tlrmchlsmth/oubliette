package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const SchemaVersion = "oubliette-evidence/v1alpha1"

var (
	runIDPattern   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`(?i)["']?client-key-data["']?[ \t]*:[ \t]*["']?[^[:space:]"',}]+`),
		regexp.MustCompile(`(?i)["']?(token|password|client[_-]?secret|api[_-]?key|access[_-]?key|secret[_-]?key)["']?[ \t]*[:=][ \t]*["']?[^[:space:]"',}]+`),
		regexp.MustCompile(`(?i)authorization[ \t]*:[ \t]*bearer[ \t]+`),
	}
	requiredRunArtifacts = []string{
		"admission/kueue.json",
		"benchmark/inputs.json",
		"benchmark/results.json",
		"benchmark/stdout.txt",
		"collector/identity.json",
		"lineage/objects.json",
		"metrics/queries.json",
		"metrics/samples.json",
		"placement/pods.json",
		"profiles/resolved.json",
		"retention/policy.json",
		"source/rendered-manifests.yaml",
		"source/spec.json",
		"teardown/inventory.json",
		"transport/proof.json",
		"versions/components.json",
	}
	requiredArtifactFields = map[string][][]string{
		"admission/kueue.json":     {{"admitted", "workloads", "state"}},
		"benchmark/inputs.json":    {{"inputs", "parameters", "request"}},
		"benchmark/results.json":   {{"results", "metrics", "summary"}},
		"collector/identity.json":  {{"collector", "identity"}},
		"lineage/objects.json":     {{"objects", "lineage"}},
		"metrics/queries.json":     {{"queries"}},
		"metrics/samples.json":     {{"samples", "aggregates"}},
		"placement/pods.json":      {{"pods"}},
		"profiles/resolved.json":   {{"profiles", "resolved"}},
		"retention/policy.json":    {{"policy", "rawEvidenceDays"}},
		"source/spec.json":         {{"spec", "source"}},
		"teardown/inventory.json":  {{"inventory", "objects"}},
		"transport/proof.json":     {{"configuration", "config"}, {"runtime", "evidence"}},
		"versions/components.json": {{"components", "versions"}},
	}
)

type Run struct {
	RunID              string            `json:"runId"`
	Oubliette          string            `json:"oubliette"`
	OublietteUID       string            `json:"oublietteUid"`
	ProfileGenerations map[string]string `json:"profileGenerations"`
	IsolationScope     string            `json:"isolationScope"`
	TrustDomain        string            `json:"trustDomain"`
	WarmupStart        time.Time         `json:"warmupStart"`
	MeasurementStart   time.Time         `json:"measurementStart"`
	MeasurementEnd     time.Time         `json:"measurementEnd"`
	ExportedAt         time.Time         `json:"exportedAt"`
	Outcome            string            `json:"outcome"`
	InvalidationReason string            `json:"invalidationReason,omitempty"`
}

type InputArtifact struct {
	Name      string
	MediaType string
	Data      []byte
}

type Artifact struct {
	Name      string `json:"name"`
	MediaType string `json:"mediaType"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

type Manifest struct {
	SchemaVersion string     `json:"schemaVersion"`
	BundleDigest  string     `json:"bundleDigest"`
	Run           Run        `json:"run"`
	Artifacts     []Artifact `json:"artifacts"`
}

type Bundle struct {
	Manifest      Manifest
	ManifestBytes []byte
	Files         map[string][]byte
}

func Build(run Run, inputs []InputArtifact) (Bundle, error) {
	if err := validateRun(run); err != nil {
		return Bundle{}, err
	}
	ordered := append([]InputArtifact(nil), inputs...)
	slices.SortFunc(ordered, func(a, b InputArtifact) int { return strings.Compare(a.Name, b.Name) })
	manifest := Manifest{SchemaVersion: SchemaVersion, Run: normalizeRun(run), Artifacts: make([]Artifact, 0, len(ordered))}
	files := make(map[string][]byte, len(ordered))
	for _, input := range ordered {
		if err := validateArtifact(input); err != nil {
			return Bundle{}, err
		}
		if _, exists := files[input.Name]; exists {
			return Bundle{}, fmt.Errorf("duplicate evidence artifact %q", input.Name)
		}
		data := append([]byte(nil), input.Data...)
		sum := sha256.Sum256(data)
		files[input.Name] = data
		manifest.Artifacts = append(manifest.Artifacts, Artifact{Name: input.Name, MediaType: input.MediaType, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))})
	}
	unsigned, err := json.Marshal(manifest)
	if err != nil {
		return Bundle{}, err
	}
	sum := sha256.Sum256(unsigned)
	manifest.BundleDigest = "sha256:" + hex.EncodeToString(sum[:])
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Bundle{}, err
	}
	manifestBytes = append(manifestBytes, '\n')
	return Bundle{Manifest: manifest, ManifestBytes: manifestBytes, Files: files}, nil
}

// BuildRunBundle enforces the minimum portable provenance set required for a
// benchmark claim. Build remains available for lower-level bundle producers.
func BuildRunBundle(run Run, inputs []InputArtifact) (Bundle, error) {
	names := make(map[string]struct{}, len(inputs))
	for _, input := range inputs {
		names[input.Name] = struct{}{}
	}
	var missing []string
	for _, required := range requiredRunArtifacts {
		if _, exists := names[required]; !exists {
			missing = append(missing, required)
		}
	}
	if len(missing) > 0 {
		return Bundle{}, fmt.Errorf("evidence bundle is missing required artifacts: %s", strings.Join(missing, ", "))
	}
	for _, input := range inputs {
		if slices.Contains(requiredRunArtifacts, input.Name) {
			if err := validateRequiredArtifact(input); err != nil {
				return Bundle{}, err
			}
		}
	}
	return Build(run, inputs)
}

func validateRun(run Run) error {
	if !runIDPattern.MatchString(run.RunID) || run.Oubliette == "" || run.OublietteUID == "" || run.IsolationScope == "" || run.TrustDomain == "" {
		return errors.New("evidence run identity is incomplete")
	}
	if run.WarmupStart.IsZero() || run.MeasurementStart.IsZero() || run.MeasurementEnd.IsZero() || run.ExportedAt.IsZero() ||
		run.MeasurementStart.Before(run.WarmupStart) || run.MeasurementEnd.Before(run.MeasurementStart) || run.ExportedAt.Before(run.MeasurementEnd) {
		return errors.New("evidence run timestamps are incomplete or unordered")
	}
	switch run.Outcome {
	case "valid":
		if run.InvalidationReason != "" {
			return errors.New("valid evidence run cannot have an invalidation reason")
		}
	case "invalid":
		if strings.TrimSpace(run.InvalidationReason) == "" {
			return errors.New("invalid evidence run requires an invalidation reason")
		}
	default:
		return errors.New("evidence outcome must be valid or invalid")
	}
	return nil
}

func normalizeRun(run Run) Run {
	run.WarmupStart = run.WarmupStart.UTC()
	run.MeasurementStart = run.MeasurementStart.UTC()
	run.MeasurementEnd = run.MeasurementEnd.UTC()
	run.ExportedAt = run.ExportedAt.UTC()
	if run.ProfileGenerations == nil {
		run.ProfileGenerations = map[string]string{}
	}
	return run
}

func validateArtifact(input InputArtifact) error {
	if input.Name == "" || input.Name != path.Clean(input.Name) || path.IsAbs(input.Name) || strings.HasPrefix(input.Name, "../") || input.MediaType == "" {
		return fmt.Errorf("invalid evidence artifact name or media type %q", input.Name)
	}
	for _, pattern := range secretPatterns {
		if pattern.Match(input.Data) {
			return fmt.Errorf("evidence artifact %q contains prohibited secret material", input.Name)
		}
	}
	if isStructuredMediaType(input.MediaType) {
		documents, err := decodeDocuments(input.Data)
		if err != nil {
			return fmt.Errorf("evidence artifact %q is not valid structured data: %w", input.Name, err)
		}
		for _, document := range documents {
			if secretObjectHasValues(document) {
				return fmt.Errorf("evidence artifact %q contains Kubernetes Secret values", input.Name)
			}
		}
	}
	if bytes.IndexByte(input.Data, 0) >= 0 && strings.HasPrefix(input.MediaType, "text/") {
		return fmt.Errorf("text evidence artifact %q contains NUL bytes", input.Name)
	}
	return nil
}

func validateRequiredArtifact(input InputArtifact) error {
	if input.Name == "benchmark/stdout.txt" {
		if strings.TrimSpace(string(input.Data)) == "" {
			return fmt.Errorf("required evidence artifact %q is empty", input.Name)
		}
		return nil
	}
	if !isStructuredMediaType(input.MediaType) {
		return fmt.Errorf("required evidence artifact %q must use a JSON or YAML media type", input.Name)
	}
	documents, err := decodeDocuments(input.Data)
	if err != nil {
		return fmt.Errorf("required evidence artifact %q is invalid: %w", input.Name, err)
	}
	if input.Name == "source/rendered-manifests.yaml" {
		for _, document := range documents {
			if meaningfulNode(mappingValue(document, "kind")) && meaningfulNode(mappingValue(document, "metadata")) {
				return nil
			}
		}
		return fmt.Errorf("required evidence artifact %q contains no rendered Kubernetes object", input.Name)
	}
	groups := requiredArtifactFields[input.Name]
	for _, document := range documents {
		if document.Kind != yaml.MappingNode {
			continue
		}
		allGroupsPresent := len(groups) > 0
		for _, alternatives := range groups {
			groupPresent := false
			for _, field := range alternatives {
				if meaningfulNode(mappingValue(document, field)) {
					groupPresent = true
					break
				}
			}
			if !groupPresent {
				allGroupsPresent = false
				break
			}
		}
		if allGroupsPresent {
			return nil
		}
	}
	return fmt.Errorf("required evidence artifact %q does not contain its required provenance fields", input.Name)
}

func isStructuredMediaType(mediaType string) bool {
	return mediaType == "application/json" || mediaType == "application/yaml" || mediaType == "text/yaml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+yaml")
}

func decodeDocuments(data []byte) ([]*yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var documents []*yaml.Node
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(document.Content) > 0 {
			documents = append(documents, document.Content[0])
		}
	}
	return documents, nil
}

func meaningfulNode(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	switch node.Kind {
	case yaml.MappingNode, yaml.SequenceNode:
		return len(node.Content) > 0
	case yaml.ScalarNode:
		return node.Tag != "!!null" && strings.TrimSpace(node.Value) != ""
	case yaml.AliasNode:
		return meaningfulNode(node.Alias)
	}
	return false
}

func secretObjectHasValues(node *yaml.Node) bool {
	if node == nil {
		return false
	}
	if node.Kind == yaml.MappingNode {
		kind := mappingValue(node, "kind")
		if kind != nil && kind.Kind == yaml.ScalarNode && strings.EqualFold(kind.Value, "Secret") {
			for _, field := range []string{"data", "stringData"} {
				if meaningfulNode(mappingValue(node, field)) {
					return true
				}
			}
		}
	}
	for _, child := range node.Content {
		if secretObjectHasValues(child) {
			return true
		}
	}
	return false
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return nil
}
