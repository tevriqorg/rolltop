// Package mailhub connects the Mock source to Rolltop's existing mirror stores.
// Callers serialize sync/mutations. Native identity and cursors stay in provider
// tables; the legacy UID column is only a stable local compatibility slot.
package mailhub

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"rolltop/backend/blob"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type Service struct {
	Store  *store.Store
	Blobs  *blob.Store
	Search *search.Service
}

func (s Service) Source(ctx context.Context, a provider.Account) (provider.MockMailbox, error) {
	p := provider.MockMailbox{Account: a}
	journal, err := s.Store.ProviderJournal(ctx, a.UserID, a.ID)
	if err != nil {
		return p, err
	}
	for _, data := range journal {
		var change provider.Mutation
		if err := json.Unmarshal([]byte(data), &change); err != nil {
			return p, err
		}
		p.Journal = append(p.Journal, change)
	}
	return p, nil
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
	data, err := json.Marshal(change)
	if err != nil {
		return err
	}
	if err = s.Store.AppendProviderMutation(ctx, a.UserID, a.ID, string(data)); err != nil {
		return err
	}
	// Source mutation is durable before updating the mirror. A failed projection
	// leaves the checkpoint unchanged and is replayed by the next Sync.
	return s.Sync(ctx, a)
}
func (s Service) Sync(ctx context.Context, a provider.Account) (result error) {
	if a.Provider != provider.Mock || (a.ID != "personal" && a.ID != "work") || a.UserID <= 0 {
		return errors.New("unknown mock account")
	}
	if s.Search == nil || s.Blobs == nil {
		return errors.New("mock sync requires blob and search services")
	}
	source, err := s.Store.EnsureMockSource(ctx, a.UserID, a.ID)
	if err != nil {
		return err
	}
	p, err := s.Source(ctx, a)
	if err != nil {
		return err
	}
	run, err := s.Store.CreateSyncRun(ctx, a.UserID, source.AccountID)
	if err != nil {
		return err
	}
	progress := store.SyncProgress{CurrentMailbox: "Mock " + a.ID}
	defer func() {
		status := "completed"
		message := ""
		if result != nil {
			status = "failed"
			message = "Mock ingestion failed; retry sync"
		}
		result = errors.Join(result, s.Store.FinishSyncRun(context.WithoutCancel(ctx), a.UserID, run.ID, status, progress, message))
	}()
	existing, err := s.Store.ProviderMessages(ctx, a.UserID)
	if err != nil {
		return err
	}
	slots := map[string]uint32{}
	for _, m := range existing {
		if m.Account == a.ID {
			row, e := s.Store.GetMessageForUser(ctx, a.UserID, m.ID)
			if e != nil {
				return e
			}
			slots[m.NativeID] = row.UID
		}
	}
	nextSlot := uint32(1)
	for _, slot := range slots {
		if slot >= nextSlot {
			nextSlot = slot + 1
		}
	}
	// Retry a projection after a crash even when SQLite committed its last row
	// but the checkpoint did not advance. CreateMessage's slot uniqueness reuses it.
	state := provider.State{Account: a, Cursor: source.Cursor}
	router, err := provider.NewRouter(p)
	if err != nil {
		return err
	}
	next, err := router.Fetch(ctx, a, state, func(m provider.Message) error {
		progress.MessagesSeen++
		slot, ok := slots[m.ID]
		if !ok {
			slot = nextSlot
			nextSlot++
			slots[m.ID] = slot
		}
		flags := []string{}
		if m.Read {
			flags = append(flags, `\Seen`)
		}
		if m.Starred {
			flags = append(flags, `\Flagged`)
		}
		ingest := syncer.Service{Store: s.Store, Blobs: s.Blobs, Search: s.Search}
		msg, err := ingest.StoreSnapshotMessage(ctx, a.UserID, store.MailAccount{ID: source.AccountID, UserID: a.UserID}, store.Mailbox{ID: source.MailboxID, UserID: a.UserID, AccountID: source.AccountID, Name: "Mock mirror", IncludeInSearch: true}, syncer.FetchedMessage{Mailbox: "Mock mirror", UID: slot, Raw: m.Raw, Size: m.Size, Flags: flags, Oversized: m.Oversized})
		if err != nil {
			return err
		}
		folders, _ := json.Marshal(m.Folders)
		labels, _ := json.Marshal(m.Labels)
		if err = s.Store.SaveProviderMessage(ctx, a.UserID, store.ProviderMessage{ID: msg.ID, Account: a.ID, NativeID: m.ID, ThreadID: m.ThreadID, Folders: string(folders), Labels: string(labels), Oversized: m.Oversized}, m.Read, m.Starred); err != nil {
			return err
		}
		progress.MessagesStored++
		return s.Store.UpdateSyncRunProgress(ctx, a.UserID, run.ID, progress)
	})
	if err != nil {
		return err
	}
	return s.Store.SetProviderCursor(ctx, a.UserID, a.ID, next.Cursor)
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
	metadata, err := s.Store.ProviderMessages(ctx, userID)
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
		if f.Provider != "" && f.Provider != "mock" || f.Account != "" && f.Account != meta.Account || f.Query != "" && !hits[meta.ID] {
			continue
		}
		msg, e := s.Store.GetMessageForUser(ctx, userID, meta.ID)
		if e != nil {
			return nil, e
		}
		m := Message{ID: msg.ID, Provider: provider.Mock, Account: meta.Account, NativeID: meta.NativeID, ThreadID: meta.ThreadID, Subject: msg.Subject, From: msg.FromAddr, Body: msg.BodyText, Read: msg.IsRead, Starred: msg.IsStarred, Oversized: meta.Oversized, Attachments: []Attachment{}}
		if e = json.Unmarshal([]byte(meta.Folders), &m.Folders); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(meta.Labels), &m.Labels); e != nil {
			return nil, e
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
