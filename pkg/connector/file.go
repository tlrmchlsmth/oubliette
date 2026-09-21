package connector

import (
	"errors"
	"os"
	"path/filepath"
)

// FileSink owns a newly created directory on a trusted consumer filesystem.
// Never place it under a directory writable by an agent. Consumers must watch
// the directory (atomic rename replaces the inode), not bind-mount one file.
type FileSink struct{ directory string }

func NewFileSink(directory string) (*FileSink, error) {
	if !filepath.IsAbs(directory) {
		return nil, ErrDelivery
	}
	// Exclusive creation rejects existing directories and symlinks, and prevents
	// two connector processes from sharing a credential destination.
	if err := os.Mkdir(directory, 0700); err != nil {
		return nil, ErrDelivery
	}
	return &FileSink{directory: directory}, nil
}

func (s *FileSink) Write(data []byte) error {
	f, err := os.CreateTemp(s.directory, ".credential-")
	if err != nil {
		return ErrDelivery
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return ErrDelivery
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrDelivery
	}
	if err = f.Close(); err != nil {
		return ErrDelivery
	}
	if err = os.Rename(f.Name(), filepath.Join(s.directory, "kubeconfig")); err != nil {
		return ErrDelivery
	}
	return nil
}

func (s *FileSink) Close() error {
	err := os.Remove(filepath.Join(s.directory, "kubeconfig"))
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return errors.Join(err, os.Remove(s.directory))
}

// ReadTokenFile permits rotation by trusted atomic replacement. The lifecycle
// bearer token is never accepted on a command line or printed in diagnostics.
func ReadTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 64*1024 {
		return "", ErrDenied
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", ErrDenied
	}
	return string(b), nil
}
