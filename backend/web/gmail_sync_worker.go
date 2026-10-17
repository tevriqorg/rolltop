package web

import (
	"context"
	"log"
	"time"

	"rolltop/backend/provider"
	"rolltop/backend/store"
)

const gmailHistorySyncInterval = 2 * time.Minute

// startGmailSyncWorker is intentionally inert until all three conditions hold:
// a Google connector is configured, an account has completed Census, and the
// user has explicitly approved backfill. OAuth alone never starts Gmail reads.
func (s *Server) startGmailSyncWorker() {
	if s == nil || s.googleConnector == nil || s.backgroundCtx == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(gmailHistorySyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.backgroundCtx.Done():
				return
			case <-ticker.C:
				s.syncApprovedGoogleAccounts(s.backgroundCtx)
			}
		}
	}()
}

func (s *Server) syncApprovedGoogleAccounts(ctx context.Context) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("gmail sync scan users: %v", err)
		}
		return
	}
	for _, user := range users {
		if ctx.Err() != nil {
			return
		}
		connections, err := s.store.ListAccountConnections(ctx, user.ID, s.googleConnector.ID())
		if err != nil {
			log.Printf("gmail sync list connections user_id=%d: %v", user.ID, err)
			continue
		}
		for _, connection := range connections {
			gate, gateErr := s.store.GetProviderSyncGate(ctx, user.ID, string(provider.Gmail), googleSourceID(connection))
			if store.IsNotFound(gateErr) || (gateErr == nil && !gate.BackfillApproved()) {
				continue
			}
			if gateErr != nil {
				log.Printf("gmail sync gate user_id=%d connection_id=%d: %v", user.ID, connection.ID, gateErr)
				continue
			}
			if err := s.syncApprovedGmail(ctx, user.ID, connection); err != nil {
				if ctx.Err() == nil {
					log.Printf("gmail history sync user_id=%d connection_id=%d: %v", user.ID, connection.ID, err)
				}
				continue
			}
			s.noteMailListChanged(user.ID)
		}
	}
}
