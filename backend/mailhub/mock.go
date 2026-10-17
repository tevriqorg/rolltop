// Package mailhub connects the Mock source to Rolltop's existing mirror stores.
// Callers serialize sync/mutations. Native identity and cursors stay in provider
// tables; the legacy UID column is only a stable local compatibility slot.
package mailhub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"

	"rolltop/backend/blob"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

type Service struct {
	Store  *store.Store
	Blobs  *blob.Store
	Search *search.Service
}

type mockSource interface {
	provider.MailProvider
	Snapshot(context.Context) ([]provider.Message, error)
	Mutate(context.Context, provider.Mutation) error
	Census(context.Context, provider.Account) (provider.Census, error)
}

func Accounts(userID int64) []provider.Account {
	if strings.TrimSpace(os.Getenv("ROLLTOP_MOCK_SOURCE_URL")) != "" {
		return []provider.Account{
			{UserID: userID, ID: "personal", Provider: provider.Mock},
			{UserID: userID, ID: "work", Provider: provider.Mock},
			{UserID: userID, ID: "travel", Provider: provider.Mock},
		}
	}
	return provider.MockAccounts(userID)
}

func (s Service) Source(ctx context.Context, a provider.Account) (mockSource, error) {
	if base := strings.TrimSpace(os.Getenv("ROLLTOP_MOCK_SOURCE_URL")); base != "" {
		if a.ID != "personal" && a.ID != "work" && a.ID != "travel" {
			return nil, errors.New("unknown remote mock account")
		}
		return provider.RemoteMockMailbox{Account: a, BaseURL: strings.TrimRight(base, "/") + "/accounts/" + a.ID}, nil
	}
	p := provider.MockMailbox{Account: a}
	journal, err := s.Store.ProviderJournal(ctx, a.UserID, a.ID)
	if err != nil {
		return &p, err
	}
	for _, data := range journal {
		var change provider.Mutation
		if err := json.Unmarshal([]byte(data), &change); err != nil {
			return &p, err
		}
		p.Journal = append(p.Journal, change)
	}
	return &p, nil
}
func (s Service) Mutate(ctx context.Context, a provider.Account, change provider.Mutation) error {
	if a.Provider != provider.Mock {
		return errors.New("mock provider required")
	}
	if _, err := s.Store.EnsureMockSource(ctx, a.UserID, a.ID); err != nil {
		return err
	}
	p, err := s.Source(ctx, a)
	if err != nil {
		return err
	}
	if err = p.Mutate(ctx, change); err != nil {
		return err
	}
	if strings.TrimSpace(os.Getenv("ROLLTOP_MOCK_SOURCE_URL")) == "" {
		data, err := json.Marshal(change)
		if err != nil {
			return err
		}
		if err = s.Store.AppendProviderMutation(ctx, a.UserID, a.ID, string(data)); err != nil {
			return err
		}
	}
	// Source mutation is durable before updating the mirror. A failed projection
	// leaves the checkpoint unchanged and is replayed by the next Sync.
	return s.Sync(ctx, a)
}
func (s Service) Sync(ctx context.Context, a provider.Account) error {
	if a.Provider != provider.Mock || (a.ID != "personal" && a.ID != "work" && a.ID != "travel") || a.UserID <= 0 {
		return errors.New("unknown mock account")
	}
	p, err := s.Source(ctx, a)
	if err != nil {
		return err
	}
	label, email, ok := storeMockSourceIdentity(a.ID)
	if !ok {
		return errors.New("unknown mock account")
	}
	return s.syncProviderSource(ctx, a, providerSourceSpec{Provider: provider.Mock, ID: a.ID, Label: label, Email: email}, p)
}

func storeMockSourceIdentity(id string) (label, email string, ok bool) {
	switch id {
	case "personal":
		return "Personal", "vipdonmen.personal@example.test", true
	case "work":
		return "Work", "vipdonmen.work@example.test", true
	case "travel":
		return "Travel", "vipdonmen.travel@example.test", true
	default:
		return "", "", false
	}
}

type Message struct {
	ID          int64         `json:"id"`
	Provider    provider.Kind `json:"provider"`
	Account     string        `json:"account"`
	NativeID    string        `json:"native_id"`
	ThreadID    string        `json:"thread_id"`
	Folders     []string      `json:"folders"`
	Labels      []string      `json:"labels"`
	Subject     string        `json:"subject"`
	From        string        `json:"from"`
	Body        string        `json:"body"`
	Read        bool          `json:"read"`
	Starred     bool          `json:"starred"`
	Oversized   bool          `json:"oversized"`
	Attachments []Attachment  `json:"attachments"`
}
type Attachment struct {
	ID       int64  `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
}
type Filter struct{ View, Provider, Account, Label, Query string }

func (s Service) Messages(ctx context.Context, userID int64, f Filter) ([]Message, error) {
	metadata, err := s.Store.ProviderMessagesFor(ctx, userID, "")
	if err != nil {
		return nil, err
	}
	hits := map[int64]bool{}
	if f.Query != "" {
		// Page through all matching IDs so post-search account/label filters cannot
		// silently discard valid results behind an arbitrary hit limit.
		for offset := 0; ; offset += 128 {
			ids, e := s.Search.Search(ctx, userID, f.Query, 128, offset)
			if e != nil {
				return nil, e
			}
			for _, id := range ids {
				hits[id] = true
			}
			if len(ids) < 128 {
				break
			}
		}
	}
	out := []Message{}
	for _, meta := range metadata {
		if f.Provider != "" && f.Provider != meta.Provider || f.Account != "" && f.Account != meta.Account || f.Query != "" && !hits[meta.ID] {
			continue
		}
		msg, e := s.Store.GetMessageForUser(ctx, userID, meta.ID)
		if e != nil {
			return nil, e
		}
		m := Message{ID: msg.ID, Provider: provider.Kind(meta.Provider), Account: meta.Account, NativeID: meta.NativeID, ThreadID: meta.ThreadID, Subject: msg.Subject, From: msg.FromAddr, Body: msg.BodyText, Read: msg.IsRead, Starred: msg.IsStarred, Oversized: meta.Oversized, Attachments: []Attachment{}}
		if e = json.Unmarshal([]byte(meta.Folders), &m.Folders); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(meta.Labels), &m.Labels); e != nil {
			return nil, e
		}
		// Keep the browser contract array-shaped even when provider metadata was
		// persisted from a nil slice (JSON null). UI filters and provenance views
		// should never need provider-specific null checks for folders or labels.
		if m.Folders == nil {
			m.Folders = []string{}
		}
		if m.Labels == nil {
			m.Labels = []string{}
		}
		if f.Label != "" && !slices.Contains(m.Labels, f.Label) && !slices.Contains(m.Folders, f.Label) {
			continue
		}
		trash := slices.Contains(m.Folders, "Trash")
		switch f.View {
		case "inbox":
			if trash || !slices.Contains(m.Folders, "INBOX") {
				continue
			}
		case "unread":
			if trash || m.Read {
				continue
			}
		case "starred":
			if trash || !m.Starred {
				continue
			}
		case "trash":
			if !trash {
				continue
			}
		default:
			if trash {
				continue
			}
		}
		atts, e := s.Store.ListAttachmentsForMessage(ctx, userID, msg.ID)
		if e != nil {
			return nil, e
		}
		for _, att := range atts {
			m.Attachments = append(m.Attachments, Attachment{att.ID, att.Filename, att.Size})
		}
		out = append(out, m)
	}
	return out, nil
}
