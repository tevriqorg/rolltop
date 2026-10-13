// Package provider defines the Gate B read-only routing seam. It deliberately
// does not connect providers to SQLite, search, HTTP routes, or credentials.
package provider

import (
	"context"
	"errors"
	"fmt"
)

type Kind string

const (
	Mock Kind = "mock"
	IMAP Kind = "imap"
)

// Account identifies a source within a tenant; IDs are local, not email addresses.
type Account struct {
	UserID   int64
	ID       string
	Provider Kind
}

// State is scoped to a provider, tenant, account and collection. Only the
// provider interprets Cursor: native history/page/delta tokens need no IMAP UID.
// It is an in-memory contract at Gate B, not a database migration.
type State struct {
	Account    Account
	Collection string
	Cursor     string
}

type Message struct {
	ID        string
	ThreadID  string
	MessageID string
	Folders   []string
	Labels    []string
	Read      bool
	Starred   bool
	Raw       []byte
	Size      int64
	Oversized bool // Raw is a header stub, never a complete body.
}

// MailProvider streams observations. A returned checkpoint is committed only
// after all callbacks succeed; failed runs return the input state for replay.
type MailProvider interface {
	Kind() Kind
	Fetch(context.Context, Account, State, func(Message) error) (State, error)
}

// Router has explicit registrations and no fallback to a network provider.
type Router struct{ providers map[Kind]MailProvider }

func NewRouter(providers ...MailProvider) (*Router, error) {
	r := &Router{providers: make(map[Kind]MailProvider)}
	for _, p := range providers {
		if p == nil || p.Kind() == "" {
			return nil, errors.New("provider registration requires a kind")
		}
		if _, ok := r.providers[p.Kind()]; ok {
			return nil, fmt.Errorf("duplicate provider %q", p.Kind())
		}
		r.providers[p.Kind()] = p
	}
	return r, nil
}

func (r *Router) Fetch(ctx context.Context, account Account, state State, handle func(Message) error) (State, error) {
	if err := validate(ctx, account, state, handle); err != nil {
		return state, err
	}
	if r == nil {
		return state, errors.New("provider router is required")
	}
	p, ok := r.providers[account.Provider]
	if !ok {
		return state, fmt.Errorf("unsupported provider %q", account.Provider)
	}
	return p.Fetch(ctx, account, state, handle)
}

func validate(ctx context.Context, account Account, state State, handle func(Message) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if account.UserID <= 0 || account.ID == "" || account.Provider == "" {
		return errors.New("provider requires a tenant and account")
	}
	if state.Account != account {
		return errors.New("provider state belongs to a different tenant, account or provider")
	}
	if handle == nil {
		return errors.New("provider requires a message handler")
	}
	return nil
}
