package observability

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

func TestPrometheusTargetAPIValidatesTrustedCoverage(t *testing.T) {
	set := TargetSet{Oubliette: "demo", ProfileGeneration: "metrics-v1", Expected: []Target{{Role: "rank", PodUID: "pod-a", Endpoint: "metrics:8000", Node: "node-a"}}}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/api/v1/targets" || request.URL.Query().Get("state") != "active" || request.Header.Get("Authorization") != "Bearer operator" {
			t.Fatalf("request = %s, headers = %#v", request.URL.String(), request.Header)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"activeTargets":[{"labels":{"oubliette_id":"other"},"health":"up"},{"labels":{"oubliette_id":"demo","oubliette_profile_generation":"metrics-v1","oubliette_role":"rank","oubliette_pod_uid":"pod-a","oubliette_endpoint":"metrics:8000","oubliette_node":"node-a"},"health":"up"}]}}`))}, nil
	})}
	if err := (PrometheusTargetAPI{BaseURL: "http://prometheus.invalid", Authorization: "Bearer operator", Client: client}).ValidateCoverage(t.Context(), set); err != nil {
		t.Fatal(err)
	}
}

func TestPrometheusTargetAPIRejectsDuplicateCoverage(t *testing.T) {
	set := TargetSet{Oubliette: "demo", ProfileGeneration: "metrics-v1", Expected: []Target{{Role: "rank", PodUID: "pod-a", Endpoint: "metrics:8000", Node: "node-a"}}}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":{"activeTargets":[{"labels":{"oubliette_id":"demo","oubliette_profile_generation":"metrics-v1","oubliette_role":"rank","oubliette_pod_uid":"pod-a","oubliette_endpoint":"metrics:8000","oubliette_node":"node-a"},"health":"up"},{"labels":{"oubliette_id":"demo","oubliette_profile_generation":"metrics-v1","oubliette_role":"rank","oubliette_pod_uid":"pod-a","oubliette_endpoint":"metrics:8000","oubliette_node":"node-a"},"health":"up"}]}}`))}, nil
	})}
	err := (PrometheusTargetAPI{BaseURL: "http://prometheus.invalid", Client: client}).ValidateCoverage(t.Context(), set)
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("duplicate coverage error = %v", err)
	}
}
