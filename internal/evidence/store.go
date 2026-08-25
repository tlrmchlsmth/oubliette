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
	if err := reserveRunID(s.Root, bundle.Manifest.Run.RunID, digest); err != nil {
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

func reserveRunID(root, runID, digest string) error {
	if !runIDPattern.MatchString(runID) {
		return errors.New("evidence run ID is invalid")
	}
	directory := filepath.Join(root, ".run-ids")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	index := filepath.Join(directory, runID)
	file, err := os.OpenFile(index, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		if _, writeErr := file.WriteString(digest + "\n"); writeErr != nil {
			_ = file.Close()
			return writeErr
		}
		return file.Close()
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	existing, readErr := os.ReadFile(index)
	if readErr != nil {
		return readErr
	}
	if strings.TrimSpace(string(existing)) != digest {
		return fmt.Errorf("evidence run ID %q already identifies a different bundle", runID)
	}
	return nil
}
