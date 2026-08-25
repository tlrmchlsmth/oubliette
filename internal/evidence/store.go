package evidence

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type Store interface {
	Put(context.Context, Bundle) (string, error)
}

type DirectoryStore struct {
	Root string
}

func (s DirectoryStore) Put(ctx context.Context, bundle Bundle) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if s.Root == "" || !strings.HasPrefix(bundle.Manifest.BundleDigest, "sha256:") {
		return "", errors.New("evidence store or bundle digest is invalid")
	}
	digest := strings.TrimPrefix(bundle.Manifest.BundleDigest, "sha256:")
	if len(digest) != 64 {
		return "", errors.New("evidence bundle digest is invalid")
	}
	if err := os.MkdirAll(s.Root, 0o700); err != nil {
		return "", err
	}
	target := filepath.Join(s.Root, digest)
	if existing, err := os.ReadFile(filepath.Join(target, "manifest.json")); err == nil {
		if bytes.Equal(existing, bundle.ManifestBytes) {
			return target, nil
		}
		return "", errors.New("evidence digest already exists with different content")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	temporary, err := os.MkdirTemp(s.Root, ".bundle-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(temporary)
	if err := os.WriteFile(filepath.Join(temporary, "manifest.json"), bundle.ManifestBytes, 0o600); err != nil {
		return "", err
	}
	for name, data := range bundle.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		destination := filepath.Join(temporary, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return "", err
		}
		if err := os.WriteFile(destination, data, 0o600); err != nil {
			return "", err
		}
	}
	if err := os.Rename(temporary, target); err != nil {
		if existing, readErr := os.ReadFile(filepath.Join(target, "manifest.json")); readErr == nil && bytes.Equal(existing, bundle.ManifestBytes) {
			return target, nil
		}
		return "", fmt.Errorf("publish evidence bundle: %w", err)
	}
	return target, nil
}
