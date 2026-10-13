package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"rolltop/backend/imapclient"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

// IMAPProvider embeds the concrete Fetcher so every legacy optional interface
// (generation checks, reconciliation, watch, etc.) remains available unchanged.
// Existing sync continues to use those promoted methods, not the neutral seam.
type IMAPProvider struct{ *imapclient.Fetcher }

var _ syncer.Fetcher = (*IMAPProvider)(nil)

func (*IMAPProvider) Kind() Kind { return IMAP }

type imapCursor struct {
	UIDValidity uint32 `json:"uid_validity"`
	UID         uint32 `json:"uid"`
}

// FetchIMAP exposes the neutral seam with explicit credential-bearing local
// account input; credentials never enter provider-neutral account/state types.
func (p *IMAPProvider) FetchIMAP(ctx context.Context, account Account, source store.MailAccount, state State, handle func(Message) error) (State, error) {
	if err := validate(ctx, account, state, handle); err != nil {
		return state, err
	}
	if account.Provider != IMAP || source.UserID != account.UserID || strconv.FormatInt(source.ID, 10) != account.ID {
		return state, errors.New("IMAP source account mismatch")
	}
	if p == nil || p.Fetcher == nil || state.Collection == "" {
		return state, errors.New("IMAP requires a fetcher and mailbox")
	}
	return fetchIMAP(ctx, p.Fetcher, source, state, handle)
}

// imapReader permits adapter tests without a socket or credentials.
type imapReader interface {
	MailboxStatus(context.Context, store.MailAccount, string) (syncer.MailboxStatus, error)
	FetchMailboxWithUIDValidity(context.Context, store.MailAccount, string, uint32, uint32, func(syncer.FetchedMessage) error) error
}

func fetchIMAP(ctx context.Context, reader imapReader, source store.MailAccount, state State, handle func(Message) error) (State, error) {
	cursor := imapCursor{}
	if state.Cursor != "" {
		if err := json.Unmarshal([]byte(state.Cursor), &cursor); err != nil || cursor.UIDValidity == 0 {
			return state, errors.New("invalid IMAP cursor")
		}
	} else {
		status, err := reader.MailboxStatus(ctx, source, state.Collection)
		if err != nil {
			return state, err
		}
		cursor.UIDValidity = status.UIDValidity
	}
	err := reader.FetchMailboxWithUIDValidity(ctx, source, state.Collection, cursor.UID, cursor.UIDValidity, func(m syncer.FetchedMessage) error {
		err := handle(Message{ID: fmt.Sprintf("%s:%d:%d", state.Collection, m.UIDValidity, m.UID), Folders: []string{m.Mailbox}, Raw: m.Raw, Size: m.Size, Oversized: m.Oversized, Read: hasFlag(m.Flags, "\\Seen"), Starred: hasFlag(m.Flags, "\\Flagged")})
		if err == nil && m.UID > cursor.UID {
			cursor.UID = m.UID
		}
		return err
	})
	if err != nil {
		return state, err
	}
	raw, err := json.Marshal(cursor)
	if err != nil {
		return state, err
	}
	state.Cursor = string(raw)
	return state, nil
}

// Bind creates an account-scoped MailProvider route without changing the legacy Fetcher.
// Keeping the unbound wrapper out of MailProvider makes accidental credential-less
// registration impossible while preserving every promoted IMAP capability.
func (p *IMAPProvider) Bind(source store.MailAccount) MailProvider { return boundIMAP{p, source} }

type boundIMAP struct {
	provider *IMAPProvider
	source   store.MailAccount
}

func (boundIMAP) Kind() Kind { return IMAP }
func (p boundIMAP) Fetch(ctx context.Context, account Account, state State, handle func(Message) error) (State, error) {
	return p.provider.FetchIMAP(ctx, account, p.source, state, handle)
}
func hasFlag(flags []string, flag string) bool {
	for _, f := range flags {
		if f == flag {
			return true
		}
	}
	return false
}
