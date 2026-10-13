package syncer

import (
	"context"
	"errors"
	"rolltop/backend/store"
)

// StoreSnapshotMessage reuses local ingestion without any Fetcher or network
// access. Provider-native identities and checkpoints remain with the caller.
func (s *Service) StoreSnapshotMessage(ctx context.Context, userID int64, account store.MailAccount, mailbox store.Mailbox, item FetchedMessage) (store.MessageRecord, error) {
	if userID <= 0 || account.UserID != userID || mailbox.UserID != userID || mailbox.AccountID != account.ID {
		return store.MessageRecord{}, errors.New("snapshot tenant mismatch")
	}
	msg, _, pending, err := s.storeFetchedMessage(ctx, userID, account, mailbox, item, true)
	if err != nil {
		return msg, err
	}
	batch := newFetchedSearchIndexBatch(s)
	if err = batch.Add(ctx, pending); err != nil {
		return msg, err
	}
	if err = batch.Flush(ctx); err != nil {
		return msg, err
	}
	err = s.Store.MarkMessageImportCompleted(ctx, userID, msg.ID)
	return msg, err
}
