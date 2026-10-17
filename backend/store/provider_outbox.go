package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	ProviderOutboxPending  = "pending"
	ProviderOutboxSending  = "sending"
	ProviderOutboxSent     = "sent"
	ProviderOutboxFailed   = "failed"
	ProviderOutboxUnknown  = "unknown"
	ProviderOutboxCanceled = "canceled"
)

type ProviderOutboxJob struct {
	ID              int64
	UserID          int64
	Provider        string
	SourceID        string
	SubmissionKey   string
	Subject         string
	ThreadID        string
	BlobID          int64
	RawSHA256       string
	RawSize         int64
	State           string
	NativeMessageID string
	AttemptCount    int
	LastError       string
	AcknowledgedAt  time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type ProviderOutboxEnqueue struct {
	UserID        int64
	Provider      string
	SourceID      string
	SubmissionKey string
	Subject       string
	ThreadID      string
	BlobID        int64
	RawSHA256     string
	RawSize       int64
}

func (s *Store) EnqueueProviderOutbox(ctx context.Context, in ProviderOutboxEnqueue) (ProviderOutboxJob, bool, error) {
	in.Provider = strings.ToLower(strings.TrimSpace(in.Provider))
	in.SourceID = strings.TrimSpace(in.SourceID)
	in.SubmissionKey = strings.TrimSpace(in.SubmissionKey)
	in.RawSHA256 = strings.TrimSpace(in.RawSHA256)
	if in.UserID <= 0 || in.Provider == "" || in.SourceID == "" || in.SubmissionKey == "" || len(in.SubmissionKey) > 200 || in.BlobID <= 0 || in.RawSHA256 == "" || in.RawSize <= 0 {
		return ProviderOutboxJob{}, false, errors.New("invalid provider outbox job")
	}
	db, err := s.dataDB(ctx, in.UserID)
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	now := nowUnix()
	res, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO provider_outbox_jobs(user_id,provider,source_id,submission_key,subject,thread_id,blob_id,raw_sha256,raw_size,state,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,'pending',?,?)`, in.UserID, in.Provider, in.SourceID, in.SubmissionKey, strings.TrimSpace(in.Subject), strings.TrimSpace(in.ThreadID), in.BlobID, in.RawSHA256, in.RawSize, now, now)
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	job, err := s.GetProviderOutboxBySubmission(ctx, in.UserID, in.Provider, in.SourceID, in.SubmissionKey)
	return job, n > 0, err
}

func scanProviderOutbox(row rowScanner) (ProviderOutboxJob, error) {
	var j ProviderOutboxJob
	var acknowledged, created, updated int64
	err := row.Scan(&j.ID, &j.UserID, &j.Provider, &j.SourceID, &j.SubmissionKey, &j.Subject, &j.ThreadID, &j.BlobID, &j.RawSHA256, &j.RawSize, &j.State, &j.NativeMessageID, &j.AttemptCount, &j.LastError, &acknowledged, &created, &updated)
	if err != nil {
		return ProviderOutboxJob{}, err
	}
	if acknowledged > 0 {
		j.AcknowledgedAt = unixTime(acknowledged)
	}
	j.CreatedAt = unixTime(created)
	j.UpdatedAt = unixTime(updated)
	return j, nil
}
func providerOutboxSelect() string {
	return `SELECT id,user_id,provider,source_id,submission_key,subject,thread_id,blob_id,raw_sha256,raw_size,state,native_message_id,attempt_count,last_error,acknowledged_at,created_at,updated_at FROM provider_outbox_jobs`
}
func (s *Store) GetProviderOutboxBySubmission(ctx context.Context, userID int64, providerName, sourceID, submission string) (ProviderOutboxJob, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderOutboxJob{}, err
	}
	j, err := scanProviderOutbox(db.QueryRowContext(ctx, providerOutboxSelect()+` WHERE user_id=? AND provider=? AND source_id=? AND submission_key=?`, userID, strings.ToLower(strings.TrimSpace(providerName)), strings.TrimSpace(sourceID), strings.TrimSpace(submission)))
	if err == sql.ErrNoRows {
		return ProviderOutboxJob{}, ErrNotFound
	}
	return j, err
}
func (s *Store) GetProviderOutboxJob(ctx context.Context, userID, id int64) (ProviderOutboxJob, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderOutboxJob{}, err
	}
	j, err := scanProviderOutbox(db.QueryRowContext(ctx, providerOutboxSelect()+` WHERE user_id=? AND id=?`, userID, id))
	if err == sql.ErrNoRows {
		return ProviderOutboxJob{}, ErrNotFound
	}
	return j, err
}

func (s *Store) ClaimNextProviderOutbox(ctx context.Context, userID int64) (ProviderOutboxJob, bool, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	defer tx.Rollback()
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM provider_outbox_jobs WHERE user_id=? AND state='pending' ORDER BY id LIMIT 1`, userID).Scan(&id)
	if err == sql.ErrNoRows {
		return ProviderOutboxJob{}, false, nil
	}
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	res, err := tx.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state='sending',attempt_count=attempt_count+1,last_error='',updated_at=? WHERE user_id=? AND id=? AND state='pending'`, nowUnix(), userID, id)
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n != 1 {
		return ProviderOutboxJob{}, false, err
	}
	j, err := scanProviderOutbox(tx.QueryRowContext(ctx, providerOutboxSelect()+` WHERE user_id=? AND id=?`, userID, id))
	if err != nil {
		return ProviderOutboxJob{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return ProviderOutboxJob{}, false, err
	}
	return j, true, nil
}

func (s *Store) FinishProviderOutbox(ctx context.Context, userID, id int64, state, nativeID, lastError string) error {
	switch state {
	case ProviderOutboxSent, ProviderOutboxFailed, ProviderOutboxUnknown:
	default:
		return errors.New("invalid provider outbox terminal state")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state=?,native_message_id=?,last_error=?,updated_at=? WHERE user_id=? AND id=? AND state='sending'`, state, strings.TrimSpace(nativeID), trimLimit(lastError, 1000), nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) RetryFailedProviderOutbox(ctx context.Context, userID, id int64) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state='pending',last_error='',updated_at=? WHERE user_id=? AND id=? AND state='failed'`, nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) RecoverInterruptedProviderOutbox(ctx context.Context, userID int64) (int, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state='unknown',last_error='delivery outcome unknown after process interruption',updated_at=? WHERE user_id=? AND state='sending'`, nowUnix(), userID)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (s *Store) ListProviderOutboxJobsForUser(ctx context.Context, userID int64, limit int) ([]ProviderOutboxJob, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, providerOutboxSelect()+` WHERE user_id=? ORDER BY id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderOutboxJob{}
	for rows.Next() {
		j, err := scanProviderOutbox(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}
func (s *Store) AcknowledgeProviderOutbox(ctx context.Context, userID, id int64) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET acknowledged_at=?,updated_at=? WHERE user_id=? AND id=? AND state IN ('failed','unknown')`, nowUnix(), nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
func (s *Store) CancelProviderOutbox(ctx context.Context, userID, id int64) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state='canceled',updated_at=? WHERE user_id=? AND id=? AND state IN ('pending','failed')`, nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("provider send can no longer be canceled")
	}
	return nil
}
func (s *Store) RequeueProviderOutbox(ctx context.Context, userID, id int64, retryAnyway bool) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	allowed := `state='failed'`
	if retryAnyway {
		allowed = `state IN ('failed','unknown')`
	}
	res, err := db.ExecContext(ctx, `UPDATE provider_outbox_jobs SET state='pending',last_error='',acknowledged_at=0,updated_at=? WHERE user_id=? AND id=? AND `+allowed, nowUnix(), userID, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errors.New("provider send is not waiting for a retry")
	}
	return nil
}
