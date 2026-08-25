package observability

import (
	"fmt"
	"sort"
	"strings"
)

const (
	LabelOubliette         = "oubliette_id"
	LabelProfileGeneration = "oubliette_profile_generation"
	LabelRole              = "oubliette_role"
	LabelPodUID            = "oubliette_pod_uid"
	LabelEndpoint          = "oubliette_endpoint"
	LabelNode              = "oubliette_node"
)

type Target struct {
	Role     string
	PodUID   string
	Endpoint string
	Node     string
}

type DiscoveredTarget struct {
	Health string
	Labels map[string]string
}

type TargetSet struct {
	Oubliette         string
	ProfileGeneration string
	Expected          []Target
}

// ValidateTargetCoverage proves that trusted discovery produced exactly one
// healthy scrape target for every expected serving or routing endpoint.
func ValidateTargetCoverage(set TargetSet, discovered []DiscoveredTarget) error {
	if set.Oubliette == "" || set.ProfileGeneration == "" || len(set.Expected) == 0 {
		return fmt.Errorf("expected target identity is incomplete")
	}
	want := make(map[string]Target, len(set.Expected))
	for _, target := range set.Expected {
		if target.Role == "" || target.PodUID == "" || target.Endpoint == "" || target.Node == "" {
			return fmt.Errorf("expected target identity is incomplete")
		}
		key := targetKey(target.PodUID, target.Endpoint)
		if _, exists := want[key]; exists {
			return fmt.Errorf("expected target %s is duplicated", key)
		}
		want[key] = target
	}
	seen := make(map[string]struct{}, len(discovered))
	for _, actual := range discovered {
		labels := actual.Labels
		if labels[LabelOubliette] != set.Oubliette || labels[LabelProfileGeneration] != set.ProfileGeneration {
			return fmt.Errorf("target has unexpected Oubliette or profile attribution")
		}
		key := targetKey(labels[LabelPodUID], labels[LabelEndpoint])
		expected, exists := want[key]
		if !exists {
			return fmt.Errorf("unexpected target %s", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("target %s is not unique", key)
		}
		seen[key] = struct{}{}
		if actual.Health != "up" {
			return fmt.Errorf("target %s is unhealthy", key)
		}
		if labels[LabelRole] != expected.Role || labels[LabelNode] != expected.Node {
			return fmt.Errorf("target %s has unexpected trusted attribution", key)
		}
	}
	if len(seen) != len(want) {
		missing := make([]string, 0, len(want)-len(seen))
		for key := range want {
			if _, exists := seen[key]; !exists {
				missing = append(missing, key)
			}
		}
		sort.Strings(missing)
		return fmt.Errorf("expected targets are missing: %s", strings.Join(missing, ", "))
	}
	return nil
}

func targetKey(podUID, endpoint string) string {
	if podUID == "" || endpoint == "" {
		return "<incomplete>"
	}
	return podUID + "/" + endpoint
}
