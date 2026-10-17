package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	gmailprovider "rolltop/backend/provider/gmail"
	"rolltop/backend/store"
)

const providerOutboxScanInterval = 15 * time.Second

func (s *Server) wakeProviderOutboxWorker() {
	if s == nil || s.providerOutboxWake == nil {
		return
	}
	select {
	case s.providerOutboxWake <- struct{}{}:
	default:
	}
}

func (s *Server) startProviderOutboxWorker() {
	if s == nil || s.store == nil || s.blobs == nil || s.backgroundCtx == nil {
		return
	}
	s.recoverProviderOutboxJobs(s.backgroundCtx)
	go func() {
		ticker := time.NewTicker(providerOutboxScanInterval)
		defer ticker.Stop()
		for {
			for s.processOneProviderOutboxJob(s.backgroundCtx) {
			}
			select {
			case <-s.backgroundCtx.Done():
				return
			case <-s.providerOutboxWake:
			case <-ticker.C:
			}
		}
	}()
}

func (s *Server) recoverProviderOutboxJobs(ctx context.Context) {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("recover provider outbox users: %v", err)
		}
		return
	}
	for _, u := range users {
		n, e := s.store.RecoverInterruptedProviderOutbox(ctx, u.ID)
		if e != nil {
			log.Printf("recover provider outbox user_id=%d: %v", u.ID, e)
		} else if n > 0 {
			log.Printf("provider outbox uncertain after restart user_id=%d count=%d", u.ID, n)
		}
	}
}

func (s *Server) googleConnectionForSource(ctx context.Context, userID int64, sourceID string) (store.AccountConnection, error) {
	if s.googleConnector == nil {
		return store.AccountConnection{}, errors.New("Google connector is not configured")
	}
	connections, err := s.store.ListAccountConnections(ctx, userID, s.googleConnector.ID())
	if err != nil {
		return store.AccountConnection{}, err
	}
	for _, c := range connections {
		if googleSourceID(c) == sourceID {
			return c, nil
		}
	}
	return store.AccountConnection{}, store.ErrNotFound
}

func (s *Server) processOneProviderOutboxJob(ctx context.Context) bool {
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return false
	}
	for _, u := range users {
		if ctx.Err() != nil {
			return false
		}
		job, ok, err := s.store.ClaimNextProviderOutbox(ctx, u.ID)
		if err != nil {
			log.Printf("claim provider outbox user_id=%d: %v", u.ID, err)
			continue
		}
		if !ok {
			continue
		}
		s.processClaimedProviderOutboxJob(ctx, job)
		return true
	}
	return false
}

func (s *Server) processClaimedProviderOutboxJob(ctx context.Context, job store.ProviderOutboxJob) {
	fail := func(state, message string) {
		if err := s.store.FinishProviderOutbox(context.WithoutCancel(ctx), job.UserID, job.ID, state, "", message); err != nil {
			log.Printf("finish provider outbox user_id=%d job_id=%d: %v", job.UserID, job.ID, err)
		}
	}
	if job.Provider != "gmail" {
		fail(store.ProviderOutboxFailed, "unsupported provider outbox")
		return
	}
	connection, err := s.googleConnectionForSource(ctx, job.UserID, job.SourceID)
	if err != nil {
		fail(store.ProviderOutboxFailed, "provider connection unavailable")
		return
	}
	gate, err := s.store.GetProviderSyncGate(ctx, job.UserID, "gmail", job.SourceID)
	if err != nil || !gate.BackfillApproved() {
		fail(store.ProviderOutboxFailed, "provider send approval is not active")
		return
	}
	blobRec, err := s.store.GetBlobForUser(ctx, job.UserID, job.BlobID)
	if err != nil {
		fail(store.ProviderOutboxFailed, "queued message blob unavailable")
		return
	}
	file, err := s.blobs.OpenUserBlob(job.UserID, blobRec.Path)
	if err != nil {
		fail(store.ProviderOutboxFailed, "queued message file unavailable")
		return
	}
	defer file.Close()
	if err := verifyOutboxSpool(file, job.RawSize, job.RawSHA256); err != nil {
		fail(store.ProviderOutboxFailed, "queued message failed integrity verification")
		return
	}
	if _, err := file.Seek(0, 0); err != nil {
		fail(store.ProviderOutboxFailed, "queued message could not be rewound")
		return
	}
	raw := make([]byte, job.RawSize)
	if _, err := io.ReadFull(file, raw); err != nil {
		fail(store.ProviderOutboxFailed, "queued message could not be read")
		return
	}
	client, err := s.gmailClientForConnection(ctx, job.UserID, connection)
	if err != nil {
		fail(store.ProviderOutboxFailed, "Google access token refresh failed")
		return
	}
	sent, err := client.SendRaw(ctx, raw)
	if err != nil {
		state := store.ProviderOutboxUnknown
		message := "Gmail send outcome is uncertain; automatic retry is disabled"
		var apiErr *gmailprovider.HTTPError
		if errors.As(err, &apiErr) && apiErr.StatusCode < 500 {
			state = store.ProviderOutboxFailed
			message = fmt.Sprintf("Gmail rejected send with HTTP %d", apiErr.StatusCode)
		}
		if errors.Is(err, gmailprovider.ErrUnauthorized) {
			state = store.ProviderOutboxFailed
			message = "Gmail authorization rejected send"
		}
		fail(state, message)
		return
	}
	if err := s.store.FinishProviderOutbox(context.WithoutCancel(ctx), job.UserID, job.ID, store.ProviderOutboxSent, sent.ID, ""); err != nil {
		log.Printf("provider send accepted but state update failed user_id=%d job_id=%d: %v", job.UserID, job.ID, err)
		return
	}
	// Sent is already durable. Mirror refresh is best-effort and cannot turn a
	// successful external send into a retryable send job.
	if err := s.syncApprovedGmail(ctx, job.UserID, connection); err != nil && ctx.Err() == nil {
		log.Printf("refresh Gmail after send user_id=%d job_id=%d: %v", job.UserID, job.ID, err)
	}
	s.noteMailListChanged(job.UserID)
}

func providerOutboxHTTPStatus(job store.ProviderOutboxJob) int {
	if job.State == store.ProviderOutboxFailed {
		return http.StatusBadGateway
	}
	if job.State == store.ProviderOutboxUnknown {
		return http.StatusConflict
	}
	return http.StatusAccepted
}
