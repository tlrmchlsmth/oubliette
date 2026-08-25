package metricsgateway

import (
	"strings"
	"testing"
	"time"
)

func testPolicy() Policy {
	return Policy{
		AllowedMetrics:       []string{"up", "vllm_requests_total"},
		SensitiveLabels:      []string{"node", "host_ip"},
		MaxLookback:          time.Hour,
		MinStep:              15 * time.Second,
		MaxExecutionTime:     10 * time.Second,
		MaxSamples:           100,
		MaxResponseBytes:     1 << 20,
		MaxConcurrency:       2,
		MaxRequestsPerMinute: 10,
	}
}

func TestRewriteScopesEverySelector(t *testing.T) {
	got, err := testPolicy().Rewrite("sum(rate(vllm_requests_total{role=\"decode\"}[5m])) + up", "demo", "team-a")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"vllm_requests_total{oubliette_id=\"demo\",oubliette_trust_domain=\"team-a\",role=\"decode\"}",
		"up{oubliette_id=\"demo\",oubliette_trust_domain=\"team-a\"}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rewritten query %q does not contain %q", got, want)
		}
	}
}

func TestRewriteRejectsScopeAndMetricBypass(t *testing.T) {
	for _, query := range []string{
		"up{oubliette_id=~\".*\"}",
		"process_cpu_seconds_total",
		"{__name__=~\"up|process_cpu_seconds_total\"}",
		"label_replace(up, \"oubliette_id\", \"other\", \"job\", \".*\")",
		"label_replace(up, \"copy\", \"$1\", \"node\", \"(.*)\")",
		"up{node=~\".*\"}",
		"up and on(node) vllm_requests_total",
		"up + ignoring(host_ip) vllm_requests_total",
		"up * on(job) group_left(node) vllm_requests_total",
		"sum by(node) (up)",
		"count without(host_ip) (up)",
		"sort_by_label(up, \"node\")",
		"rate(up[2h])",
		"up offset 2h",
		"up @ 0",
	} {
		if _, err := testPolicy().Rewrite(query, "demo", "team-a"); err == nil {
			t.Fatalf("Rewrite(%q) unexpectedly succeeded", query)
		}
	}
}
