package metricsgateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string) (Scope, error)

func (f resolverFunc) Resolve(ctx context.Context, authorization string) (Scope, error) {
	return f(ctx, authorization)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestGatewayScopesAndFiltersQuery(t *testing.T) {
	var (
		upstream url.Values
		audit    AuditEvent
	)

	gateway := &Gateway{
		Resolver: resolverFunc(func(_ context.Context, authorization string) (Scope, error) {
			if authorization != "Bearer token" {
				return Scope{}, ErrInvalidCredential
			}
			return Scope{Subject: "agent", Oubliette: "demo", TrustDomain: "team-a", Upstream: "http://prometheus.invalid", Policy: testPolicy()}, nil
		}),
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			upstream, err = url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader("{\"status\":\"success\",\"data\":{\"resultType\":\"vector\",\"result\":[{\"metric\":{\"job\":\"vllm\",\"node\":\"secret-node\",\"oubliette_id\":\"demo\"},\"value\":[1,\"2\"]}]}}")),
			}, nil
		})},
		Now:   func() time.Time { return time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC) },
		Audit: func(_ context.Context, event AuditEvent) { audit = event },
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/query?query="+url.QueryEscape("up"), nil)
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	effective := upstream.Get("query")
	if !strings.Contains(effective, "oubliette_id=\"demo\"") || !strings.Contains(effective, "oubliette_trust_domain=\"team-a\"") {
		t.Fatalf("upstream query = %q", effective)
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Body.String(), "secret-node") || strings.Contains(response.Body.String(), "\"node\"") {
		t.Fatalf("sensitive label leaked: %s", response.Body.String())
	}
	if audit.OriginalQuery != "up" || audit.EffectiveQuery == "" || audit.Status != http.StatusOK {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestGatewayScopesAndFiltersMetadata(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	var upstream url.Values
	gateway := &Gateway{
		Resolver: resolverFunc(func(context.Context, string) (Scope, error) {
			return Scope{Subject: "agent", Oubliette: "demo", TrustDomain: "team-a", Upstream: "http://prometheus.invalid", Policy: testPolicy()}, nil
		}),
		Client: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			upstream, err = url.ParseQuery(string(body))
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"status":"success","data":[{"__name__":"up","node":"secret-node","job":"vllm"}]}`))}, nil
		})},
		Now: func() time.Time { return now },
	}
	target := "/api/v1/series?match%5B%5D=up&start=" + url.QueryEscape(now.Add(-time.Minute).Format(time.RFC3339)) + "&end=" + url.QueryEscape(now.Format(time.RFC3339))
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if effective := upstream.Get("match[]"); !strings.Contains(effective, `oubliette_id="demo"`) || !strings.Contains(effective, `oubliette_trust_domain="team-a"`) {
		t.Fatalf("upstream selector = %q", effective)
	}
	if strings.Contains(response.Body.String(), "secret-node") || strings.Contains(response.Body.String(), `"node"`) {
		t.Fatalf("sensitive label leaked: %s", response.Body.String())
	}
}

func TestGatewayRejectsUnsafeEndpointsAndBudgets(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	gateway := &Gateway{
		Resolver: resolverFunc(func(context.Context, string) (Scope, error) {
			return Scope{Subject: "agent", Oubliette: "demo", TrustDomain: "team-a", Upstream: "http://example.invalid", Policy: testPolicy()}, nil
		}),
		Now: func() time.Time { return now },
	}
	tests := []string{
		"/api/v1/targets",
		"/api/v1/labels",
		"/api/v1/label/node/values?match%5B%5D=up",
		"/api/v1/query?query=" + url.QueryEscape("up{oubliette_id=\"other\"}"),
		"/api/v1/query_range?query=up&start=" + url.QueryEscape(now.Add(-2*time.Hour).Format(time.RFC3339)) + "&end=" + url.QueryEscape(now.Format(time.RFC3339)) + "&step=15s",
	}
	for _, target := range tests {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if response.Code < 400 || response.Code >= 500 {
			t.Fatalf("%s status = %d, body = %s", target, response.Code, response.Body.String())
		}
	}
}

func TestGatewayRateLimit(t *testing.T) {
	policy := testPolicy()
	policy.MaxRequestsPerMinute = 1
	gateway := &Gateway{
		Resolver: resolverFunc(func(context.Context, string) (Scope, error) {
			return Scope{Subject: "agent", Oubliette: "demo", TrustDomain: "team-a", Upstream: "http://example.invalid", Policy: policy}, nil
		}),
	}
	for i := range 2 {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/targets", nil)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, request)
		if i == 1 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("second request status = %d", response.Code)
		}
	}
}
