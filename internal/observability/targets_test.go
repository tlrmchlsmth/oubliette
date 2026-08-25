package observability

import (
	"strings"
	"testing"
)

func TestValidateTargetCoverage(t *testing.T) {
	set := TargetSet{
		Oubliette: "demo", ProfileGeneration: "metrics-v1",
		Expected: []Target{
			{Role: "rank", PodUID: "pod-a", Endpoint: "metrics:8000", Node: "node-a"},
			{Role: "router", PodUID: "pod-b", Endpoint: "metrics:9090", Node: "node-b"},
		},
	}
	discovered := []DiscoveredTarget{
		{Health: "up", Labels: labels(set, set.Expected[0])},
		{Health: "up", Labels: labels(set, set.Expected[1])},
	}
	if err := ValidateTargetCoverage(set, discovered); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetCoverageRejectsInvalidSets(t *testing.T) {
	base := TargetSet{Oubliette: "demo", ProfileGeneration: "metrics-v1", Expected: []Target{{Role: "rank", PodUID: "pod-a", Endpoint: "metrics:8000", Node: "node-a"}}}
	tests := []struct {
		name       string
		discovered []DiscoveredTarget
		want       string
	}{
		{name: "missing", want: "missing"},
		{name: "unhealthy", discovered: []DiscoveredTarget{{Health: "down", Labels: labels(base, base.Expected[0])}}, want: "unhealthy"},
		{name: "duplicate", discovered: []DiscoveredTarget{{Health: "up", Labels: labels(base, base.Expected[0])}, {Health: "up", Labels: labels(base, base.Expected[0])}}, want: "not unique"},
		{name: "relabeled", discovered: []DiscoveredTarget{{Health: "up", Labels: func() map[string]string {
			value := labels(base, base.Expected[0])
			value[LabelNode] = "tenant-value"
			return value
		}()}}, want: "trusted attribution"},
		{name: "cross-oubliette", discovered: []DiscoveredTarget{{Health: "up", Labels: func() map[string]string {
			value := labels(base, base.Expected[0])
			value[LabelOubliette] = "other"
			return value
		}()}}, want: "unexpected Oubliette"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateTargetCoverage(base, test.discovered)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func labels(set TargetSet, target Target) map[string]string {
	return map[string]string{
		LabelOubliette: set.Oubliette, LabelProfileGeneration: set.ProfileGeneration,
		LabelRole: target.Role, LabelPodUID: target.PodUID, LabelEndpoint: target.Endpoint, LabelNode: target.Node,
	}
}
