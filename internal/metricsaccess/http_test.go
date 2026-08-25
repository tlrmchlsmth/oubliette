package metricsaccess

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tlrmchlsmth/oubliette/internal/metricsgateway"
)

func TestHTTPHandlerIssuesCredentialToAuthenticatedConnector(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	key := []byte("0123456789abcdef0123456789abcdef")
	handler := HTTPHandler{
		Issuer: Issuer{
			Client: fakeClient(t, readyOubliette()),
			Codec:  metricsgateway.TokenCodec{Key: key, Audience: "oubliette-metrics", Now: func() time.Time { return now }},
			Now:    func() time.Time { return now },
		},
		BearerToken: "connector-authentication-token-32-bytes",
	}
	request := httptest.NewRequest(http.MethodPost, "/access/v1/credentials", strings.NewReader(`{"subject":"agent-1","oubliette":"demo","placement":"external","ttlSeconds":60}`))
	request.Header.Set("Authorization", "Bearer connector-authentication-token-32-bytes")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	var issued issueResponse
	if err := json.Unmarshal(response.Body.Bytes(), &issued); err != nil {
		t.Fatal(err)
	}
	claims, err := (metricsgateway.TokenCodec{Key: key, Audience: "oubliette-metrics", Now: func() time.Time { return now }}).Validate(issued.Credential)
	if err != nil || claims.Subject != "agent-1" || issued.EndpointIdentity != "metrics:demo" {
		t.Fatalf("issued=%#v claims=%#v error=%v", issued, claims, err)
	}
}

func TestHTTPHandlerRejectsUnauthenticatedConnector(t *testing.T) {
	handler := HTTPHandler{BearerToken: "connector-authentication-token-32-bytes"}
	request := httptest.NewRequest(http.MethodPost, "/access/v1/credentials", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}
