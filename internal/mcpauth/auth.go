package mcpauth

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/tlrmchlsmth/oubliette/internal/lifecycle"
	authenticationv1 "k8s.io/api/authentication/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const DefaultAudience = "oubliette-mcp"

var ErrInvalidCredential = errors.New("invalid lifecycle credential")

// Resolver authenticates an MCP bearer token and returns its stable caller
// identity. Implementations must not return raw credential material.
type Resolver interface {
	Resolve(context.Context, string) (string, error)
}

// TokenReviewClient is the narrow Kubernetes API surface needed to validate an
// audience-bound service-account token.
type TokenReviewClient interface {
	Create(context.Context, client.Object, ...client.CreateOption) error
}

type KubernetesResolver struct {
	Client   TokenReviewClient
	Audience string
}

func (r KubernetesResolver) Resolve(ctx context.Context, token string) (string, error) {
	if r.Client == nil || r.Audience == "" || token == "" {
		return "", ErrInvalidCredential
	}
	review := &authenticationv1.TokenReview{
		Spec: authenticationv1.TokenReviewSpec{
			Token:     token,
			Audiences: []string{r.Audience},
		},
	}
	if err := r.Client.Create(ctx, review); err != nil {
		return "", errors.Join(ErrInvalidCredential, err)
	}
	if !review.Status.Authenticated || review.Status.User.Username == "" || !contains(review.Status.Audiences, r.Audience) {
		return "", ErrInvalidCredential
	}
	return review.Status.User.Username, nil
}

// Authenticate validates a bearer credential before attaching its caller
// identity to the request context consumed by lifecycle operations.
func Authenticate(resolver Resolver, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		authorization := r.Header.Get("Authorization")
		if resolver == nil || !strings.HasPrefix(authorization, prefix) || len(authorization) == len(prefix) {
			unauthorized(w)
			return
		}
		caller, err := resolver.Resolve(r.Context(), strings.TrimPrefix(authorization, prefix))
		if err != nil || caller == "" {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r.WithContext(lifecycle.WithCaller(r.Context(), caller)))
	})
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
