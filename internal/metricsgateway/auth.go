package metricsgateway

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalidCredential = errors.New("invalid metrics credential")
	ErrExpiredCredential = errors.New("expired metrics credential")
)

type Claims struct {
	Subject     string `json:"sub"`
	Oubliette   string `json:"oubliette"`
	TrustDomain string `json:"trustDomain"`
	Audience    string `json:"aud"`
	IssuedUnix  int64  `json:"iat"`
	ExpiresUnix int64  `json:"exp"`
}

type TokenCodec struct {
	Key      []byte
	Audience string
	Now      func() time.Time
	MaxTTL   time.Duration
}

func (c TokenCodec) Issue(claims Claims) (string, error) {
	if len(c.Key) < 32 {
		return "", errors.New("metrics token key must contain at least 32 bytes")
	}
	if claims.IssuedUnix == 0 {
		claims.IssuedUnix = c.now().Unix()
	}
	if claims.Subject == "" || claims.Oubliette == "" || claims.TrustDomain == "" || claims.Audience != c.Audience || claims.ExpiresUnix <= claims.IssuedUnix || time.Duration(claims.ExpiresUnix-claims.IssuedUnix)*time.Second > c.maxTTL() {
		return "", errors.New("incomplete or invalid metrics token claims")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	return encoded + "." + base64.RawURLEncoding.EncodeToString(c.sign(encoded)), nil
}

func (c TokenCodec) Validate(token string) (Claims, error) {
	var claims Claims
	if len(c.Key) < 32 {
		return claims, ErrInvalidCredential
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return claims, ErrInvalidCredential
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, c.sign(parts[0])) {
		return claims, ErrInvalidCredential
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(payload, &claims) != nil {
		return Claims{}, ErrInvalidCredential
	}
	now := c.now().Unix()
	if claims.Subject == "" || claims.Oubliette == "" || claims.TrustDomain == "" || claims.Audience != c.Audience || claims.IssuedUnix > now+30 || claims.ExpiresUnix <= claims.IssuedUnix || time.Duration(claims.ExpiresUnix-claims.IssuedUnix)*time.Second > c.maxTTL() {
		return Claims{}, ErrInvalidCredential
	}
	if claims.ExpiresUnix <= c.now().Unix() {
		return Claims{}, ErrExpiredCredential
	}
	return claims, nil
}

func (c TokenCodec) sign(payload string) []byte {
	mac := hmac.New(sha256.New, c.Key)
	_, _ = mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func (c TokenCodec) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c TokenCodec) maxTTL() time.Duration {
	if c.MaxTTL > 0 {
		return c.MaxTTL
	}
	return 15 * time.Minute
}

type ScopeResolver interface {
	Resolve(context.Context, string) (Scope, error)
}

type TokenResolver struct {
	Codec         TokenCodec
	ResolveClaims func(context.Context, Claims) (Scope, error)
}

func (r TokenResolver) Resolve(ctx context.Context, authorization string) (Scope, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return Scope{}, ErrInvalidCredential
	}
	claims, err := r.Codec.Validate(strings.TrimPrefix(authorization, prefix))
	if err != nil {
		return Scope{}, err
	}
	if r.ResolveClaims == nil {
		return Scope{}, fmt.Errorf("resolve metrics claims: %w", ErrInvalidCredential)
	}
	scope, err := r.ResolveClaims(ctx, claims)
	if err != nil {
		return Scope{}, err
	}
	if scope.Oubliette != claims.Oubliette || scope.TrustDomain != claims.TrustDomain {
		return Scope{}, ErrInvalidCredential
	}
	scope.Subject = claims.Subject
	return scope, nil
}
