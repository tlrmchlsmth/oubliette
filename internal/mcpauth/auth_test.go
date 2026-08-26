package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tlrmchlsmth/oubliette/internal/lifecycle"
	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type tokenReviewClientFunc func(context.Context, client.Object, ...client.CreateOption) error

func (f tokenReviewClientFunc) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return f(ctx, obj, opts...)
}

type resolverFunc func(context.Context, string) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, token string) (string, error) {
	return f(ctx, token)
}

func TestKubernetesResolverRequiresAuthenticatedMatchingAudience(t *testing.T) {
	tests := []struct {
		name       string
		status     authenticationv1.TokenReviewStatus
		createErr  error
		wantCaller string
	}{
		{name: "valid", status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{DefaultAudience}, User: authenticationv1.UserInfo{Username: "system:serviceaccount:consumer:agent"}}, wantCaller: "system:serviceaccount:consumer:agent"},
		{name: "wrong audience", status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{"host-api"}, User: authenticationv1.UserInfo{Username: "caller"}}},
		{name: "unauthenticated", status: authenticationv1.TokenReviewStatus{Authenticated: false, Audiences: []string{DefaultAudience}, User: authenticationv1.UserInfo{Username: "caller"}}},
		{name: "empty identity", status: authenticationv1.TokenReviewStatus{Authenticated: true, Audiences: []string{DefaultAudience}}},
		{name: "review error", createErr: errors.New("review failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := KubernetesResolver{
				Audience: DefaultAudience,
				Client: tokenReviewClientFunc(func(_ context.Context, obj client.Object, _ ...client.CreateOption) error {
					review := obj.(*authenticationv1.TokenReview)
					if len(review.Spec.Audiences) != 1 || review.Spec.Audiences[0] != DefaultAudience || review.Spec.Token != "credential" {
						t.Fatalf("unexpected review spec: %#v", review.Spec)
					}
					review.Status = tt.status
					return tt.createErr
				}),
			}
			caller, err := resolver.Resolve(context.Background(), "credential")
			if tt.wantCaller == "" {
				if !errors.Is(err, ErrInvalidCredential) {
					t.Fatalf("Resolve() error = %v, want invalid credential", err)
				}
				return
			}
			if err != nil || caller != tt.wantCaller {
				t.Fatalf("Resolve() = %q, %v; want %q, nil", caller, err, tt.wantCaller)
			}
		})
	}
}

func TestAuthenticateAttachesCallerAndRejectsInvalidRequests(t *testing.T) {
	resolver := resolverFunc(func(_ context.Context, token string) (string, error) {
		if token != "valid" {
			return "", ErrInvalidCredential
		}
		return "consumer-a", nil
	})
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, err := lifecycle.Caller(r.Context())
		if err != nil || caller != "consumer-a" {
			t.Fatalf("caller = %q, %v", caller, err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	handler := Authenticate(resolver, next)

	for _, authorization := range []string{"", "Basic valid", "Bearer ", "Bearer invalid"} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", authorization)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q: status = %d, want 401", authorization, response.Code)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer valid")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("valid credential status = %d, want 204", response.Code)
	}
}
