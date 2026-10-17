package mailhub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"rolltop/backend/evidence"
	"rolltop/backend/provider"
	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type providerSourceSpec struct {
	Provider provider.Kind
	ID       string
	Label    string
	Email    string
}

// syncProviderSource projects one provider-native account into Rolltop's mutable
// Mirror while Evidence remains append-only. A provider checkpoint advances only
// after every observation has been projected successfully.
func (s Service) syncProviderSource(ctx context.Context, a provider.Account, spec providerSourceSpec, p provider.MailProvider) (result error) {
	if a.UserID <= 0 || a.Provider != spec.Provider || a.ID != spec.ID || p == nil || p.Kind() != a.Provider {
		return errors.New("provider sync scope mismatch")
	}
	if s.Search == nil || s.Blobs == nil || s.Store == nil {
		return errors.New("provider sync requires store, blob, and search services")
	}
	source, err := s.Store.EnsureProviderSource(ctx, a.UserID, string(a.Provider), a.ID, spec.Label, spec.Email)
	if err != nil {
		return err
	}
	run, err := s.Store.CreateSyncRun(ctx, a.UserID, source.AccountID)
	if err != nil {
		return err
	}
	progress := store.SyncProgress{CurrentMailbox: "INBOX"}
	defer func() {
		status, message := "completed", ""
		if result != nil {
			status = "failed"
			message = fmt.Sprintf("%s ingestion failed; retry sync", a.Provider)
		}
		result = errors.Join(result, s.Store.FinishSyncRun(context.WithoutCancel(ctx), a.UserID, run.ID, status, progress, message))
	}()
	existing, err := s.Store.ProviderMessagesFor(ctx, a.UserID, string(a.Provider))
	if err != nil {
		return err
	}
	slots := map[string]uint32{}
	stale := map[string]int64{}
	for _, m := range existing {
		if m.Account == a.ID {
			row, e := s.Store.GetMessageForUser(ctx, a.UserID, m.ID)
			if e != nil {
				return e
			}
			slots[m.NativeID] = row.UID
			stale[m.NativeID] = m.ID
		}
	}
	nextSlot := uint32(1)
	for _, slot := range slots {
		if slot >= nextSlot {
			nextSlot = slot + 1
		}
	}
	fullSnapshot := source.Cursor == ""
	state := provider.State{Account: a, Cursor: source.Cursor}
	router, err := provider.NewRouter(p)
	if err != nil {
		return err
	}
	next, err := router.Fetch(ctx, a, state, func(m provider.Message) error {
		progress.MessagesSeen++
		if m.Deleted {
			localID, e := s.Store.DeleteProviderMessage(ctx, a.UserID, string(a.Provider), a.ID, m.ID)
			if e != nil {
				return e
			}
			if localID > 0 {
				if e := s.Search.DeleteMessage(ctx, a.UserID, localID); e != nil {
					return e
				}
				progress.MessagesStored++
			}
			delete(stale, m.ID)
			return s.Store.UpdateSyncRunProgress(ctx, a.UserID, run.ID, progress)
		}
		delete(stale, m.ID)
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
		evidenceStore := evidence.Service{Store: s.Store, Root: s.Blobs.Root}
		observation, e := evidenceStore.Capture(ctx, evidence.Input{UserID: a.UserID, Provider: a.Provider, SourceAccount: a.ID, ProviderMessageID: m.ID, ThreadID: m.ThreadID, Folders: m.Folders, Labels: m.Labels, MessageIDHeader: m.MessageID, SourceSize: m.Size, Raw: m.Raw, Oversized: m.Oversized})
		if e != nil {
			return e
		}
		ingest := syncer.Service{Store: s.Store, Blobs: s.Blobs, Search: s.Search}
		msg, e := ingest.StoreSnapshotMessage(ctx, a.UserID, store.MailAccount{ID: source.AccountID, UserID: a.UserID}, store.Mailbox{ID: source.MailboxID, UserID: a.UserID, AccountID: source.AccountID, Name: "INBOX", Role: "inbox", IncludeInSearch: true}, syncer.FetchedMessage{Mailbox: "INBOX", UID: slot, Raw: m.Raw, Size: m.Size, Flags: flags, Oversized: m.Oversized})
		if e != nil {
			return e
		}
		if e = s.Store.LinkEvidenceObservation(ctx, a.UserID, observation.ObservationKey, msg.ID); e != nil {
			return e
		}
		folders, _ := json.Marshal(m.Folders)
		labels, _ := json.Marshal(m.Labels)
		if e = s.Store.SaveProviderMessage(ctx, a.UserID, store.ProviderMessage{ID: msg.ID, Provider: string(a.Provider), Account: a.ID, NativeID: m.ID, ThreadID: m.ThreadID, Folders: string(folders), Labels: string(labels), Oversized: m.Oversized}, m.Read, m.Starred); e != nil {
			return e
		}
		progress.MessagesStored++
		return s.Store.UpdateSyncRunProgress(ctx, a.UserID, run.ID, progress)
	})
	if err != nil {
		return err
	}
	if fullSnapshot {
		for nativeID, localID := range stale {
			if _, e := s.Store.DeleteProviderMessage(ctx, a.UserID, string(a.Provider), a.ID, nativeID); e != nil {
				return e
			}
			if e := s.Search.DeleteMessage(ctx, a.UserID, localID); e != nil {
				return e
			}
		}
	}
	return s.Store.SetProviderCursorFor(ctx, a.UserID, string(a.Provider), a.ID, next.Cursor)
}
