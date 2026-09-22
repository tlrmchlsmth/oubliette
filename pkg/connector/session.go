// Package connector implements the trusted consumer side of virtual API access.
// It must never run in an agent sandbox or be exposed as an agent-callable tool.
package connector

import (
	"context"
	"errors"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

var (
	ErrDenied     = errors.New("virtual access denied: caller, ownership, readiness or lease is invalid")
	ErrTransport  = errors.New("private virtual API connection failed")
	ErrCredential = errors.New("virtual credential issuance failed")
	ErrDelivery   = errors.New("private credential delivery failed")
)

type Lease struct {
	Name, Namespace, Caller string
	UID                     types.UID
	ExpiresAt               time.Time
}

type Credential struct {
	Kubeconfig []byte
	ExpiresAt  time.Time
}

type Backend interface {
	// Check reauthenticates the caller and reads authoritative lifecycle state.
	Check(context.Context) (Lease, error)
	Open(context.Context, Lease) (Connection, error)
}

type Connection interface {
	Issue(context.Context) (Credential, error)
	Done() <-chan struct{}
	Close()
}

// Sink is owned by the consumer, outside the model transcript. Implementations
// must replace credentials atomically and remove them on Close.
type Sink interface {
	Write([]byte) error
	Close() error
}

type Session struct {
	Backend Backend
	Sink    Sink
	// Ready is a credential-free notification after the first successful handoff.
	Ready func()
	// PollInterval may shorten, but never lengthen, the 15-second check interval.
	PollInterval time.Duration
	now          func() time.Time
}

// Run caps a connection at the lease observed on entry. A renewed Oubliette can
// be attached again; renewal never silently extends an existing access session.
// Errors deliberately exclude upstream bodies, tokens and kubeconfig contents.
func (s Session) Run(ctx context.Context) (result error) {
	if s.Backend == nil || s.Sink == nil {
		return ErrDenied
	}
	defer func() {
		if err := s.Sink.Close(); err != nil {
			result = errors.Join(result, ErrDelivery)
		}
	}()
	now := s.now
	if now == nil {
		now = time.Now
	}
	lease, err := s.Backend.Check(ctx)
	if err != nil || lease.UID == "" || !now().Before(lease.ExpiresAt) {
		return ErrDenied
	}
	ctx, cancel := context.WithDeadline(ctx, lease.ExpiresAt)
	defer cancel()
	connection, err := s.Backend.Open(ctx, lease)
	if err != nil {
		return ErrTransport
	}
	defer connection.Close()
	// Cancel in-flight issuance as soon as the port-forward terminates.
	go func() {
		select {
		case <-connection.Done():
			cancel()
		case <-ctx.Done():
		}
	}()

	check := func() error {
		current, err := s.Backend.Check(ctx)
		if err != nil || current.UID != lease.UID || current.Caller != lease.Caller ||
			current.Name != lease.Name || current.Namespace != lease.Namespace ||
			!now().Before(current.ExpiresAt) {
			return ErrDenied
		}
		// A shortened lease closes access immediately rather than trusting the
		// original deadline. Longer leases require a new connection.
		if current.ExpiresAt.Before(lease.ExpiresAt) {
			return ErrDenied
		}
		return ctx.Err()
	}
	issue := func() (time.Time, error) {
		if err := check(); err != nil {
			return time.Time{}, ErrDenied
		}
		credential, err := connection.Issue(ctx)
		if err != nil {
			return time.Time{}, ErrCredential
		}
		remaining := credential.ExpiresAt.Sub(now())
		if len(credential.Kubeconfig) == 0 || remaining < time.Minute || remaining > 11*time.Minute {
			return time.Time{}, ErrCredential
		}
		if err := check(); err != nil {
			return time.Time{}, ErrDenied
		}
		if err := s.Sink.Write(credential.Kubeconfig); err != nil {
			return time.Time{}, ErrDelivery
		}
		return now().Add(remaining / 2), nil
	}
	refreshAt, err := issue()
	if err != nil {
		return err
	}
	if s.Ready != nil {
		s.Ready()
	}
	interval := s.PollInterval
	if interval <= 0 || interval > 15*time.Second {
		interval = 15 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := check(); err != nil {
				return ErrDenied
			}
			if !now().Before(refreshAt) {
				refreshAt, err = issue()
				if err != nil {
					return err
				}
			}
		}
	}
}
