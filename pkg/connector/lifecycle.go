package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/tlrmchlsmth/oubliette/internal/lifecycle"
)

// Lifecycle is a trusted-consumer boundary, not an arbitrary Kubernetes proxy.
// Every call authenticates the host-held lifecycle token and uses the same
// ownership checks and metadata projections as the provider's MCP server.
func (b *KubernetesBackend) Lifecycle(ctx context.Context, tool string, arguments []byte) (json.RawMessage, error) {
	if b.Host == nil || b.Resolver == nil || b.Token == nil {
		return nil, ErrDenied
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	token, err := b.Token()
	if err != nil {
		return nil, ErrDenied
	}
	caller, err := b.Resolver.Resolve(ctx, token)
	if err != nil || caller == "" {
		return nil, ErrDenied
	}
	ctx = lifecycle.WithCaller(ctx, caller)
	service := &lifecycle.Service{Client: b.Host}
	var result any
	switch tool {
	case "oubliette_create":
		var in lifecycle.CreateInput
		if err = decodeLifecycle(arguments, &in); err == nil {
			result, err = service.Create(ctx, in)
		}
	case "oubliette_get":
		var in lifecycle.NameInput
		if err = decodeLifecycle(arguments, &in); err == nil {
			result, err = service.Get(ctx, in)
		}
	case "oubliette_list":
		var in lifecycle.ListInput
		if err = decodeLifecycle(arguments, &in); err == nil {
			result, err = service.List(ctx, in)
		}
	case "oubliette_renew":
		var in lifecycle.RenewInput
		if err = decodeLifecycle(arguments, &in); err == nil {
			result, err = service.Renew(ctx, in)
		}
	case "oubliette_delete":
		var in lifecycle.NameInput
		if err = decodeLifecycle(arguments, &in); err == nil {
			result, err = service.Delete(ctx, in)
		}
	default:
		return nil, errors.New("unsupported lifecycle operation")
	}
	if err != nil {
		return nil, errors.New("lifecycle operation rejected; check ownership, name, tier and TTL")
	}
	return json.Marshal(result)
}

func decodeLifecycle(data []byte, value any) error {
	if len(data) > 65536 || len(bytes.TrimSpace(data)) == 0 || bytes.TrimSpace(data)[0] != '{' {
		return ErrDenied
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return ErrDenied
	}
	return nil
}
