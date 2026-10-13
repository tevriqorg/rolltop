package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	EvidenceCaptureComplete  = "complete"
	EvidenceCaptureOversized = "oversized_stub"
	EvidenceCaptureFailed    = "failed"
)

type EvidenceObservationInput struct {
	UserID            int64
	ObservationKey    string
	Provider          string
	SourceAccount     string
	ProviderMessageID string
	ThreadID          string
	FoldersJSON       string
	LabelsJSON        string
	IMAPUID           *uint32
	IMAPUIDValidity   *uint32
	MessageIDHeader   string
	SourceSize        int64
	RawSHA256         string
	RawPath           string
	RawSize           int64
	CaptureStatus     string
	MirrorMessageID   int64
	LastError         string
}

type EvidenceObservation struct {
	ID                int64
	UserID            int64
	ObservationKey    string
	Provider          string
	SourceAccount     string
	ProviderMessageID string
	ThreadID          string
	FoldersJSON       string
	LabelsJSON        string
	IMAPUID           *uint32
	IMAPUIDValidity   *uint32
	MessageIDHeader   string
	FirstObservedAt   time.Time
	SourceSize        int64
	RawSHA256         string
	RawPath           string
	RawSize           int64
	CaptureStatus     string
	MirrorMessageID   int64
	LastError         string
}

type EvidenceEvent struct {
	ID            int64
	UserID        int64
	ObservationID int64
	EventKey      string
	EventType     string
	Payload       string
	CreatedAt     time.Time
	AvailableAt   time.Time
	Attempts      int
	DeliveredAt   time.Time
	LastError     string
}

func (s *Store) RecordEvidenceObservation(ctx context.Context, in EvidenceObservationInput) (EvidenceObservation, bool, error) {
	if err := validateEvidenceObservationInput(in); err != nil {
		return EvidenceObservation{}, false, err
	}
	db, err := s.dataDB(ctx, in.UserID)
	if err != nil {
		return EvidenceObservation{}, false, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return EvidenceObservation{}, false, err
	}
	defer tx.Rollback()
	if in.MirrorMessageID > 0 {
		var owner int64
		if err := tx.QueryRowContext(ctx, `SELECT user_id FROM messages WHERE id = ?`, in.MirrorMessageID).Scan(&owner); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return EvidenceObservation{}, false, ErrNotFound
			}
			return EvidenceObservation{}, false, err
		}
		if owner != in.UserID {
			return EvidenceObservation{}, false, errors.New("evidence mirror message belongs to another tenant")
		}
	}
	now := nowUnix()
	res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO evidence_observations
 (user_id, observation_key, provider, source_account, provider_message_id, thread_id, folders_json, labels_json,
  imap_uid, imap_uid_validity, message_id_header, first_observed_at, source_size, raw_sha256, raw_path, raw_size,
  capture_status, mirror_message_id, last_error)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.UserID, in.ObservationKey, in.Provider, in.SourceAccount, in.ProviderMessageID, in.ThreadID, in.FoldersJSON, in.LabelsJSON,
		nullUint32(in.IMAPUID), nullUint32(in.IMAPUIDValidity), in.MessageIDHeader, now, in.SourceSize, in.RawSHA256, in.RawPath, in.RawSize,
		in.CaptureStatus, nullInt64(in.MirrorMessageID), in.LastError)
	if err != nil {
		return EvidenceObservation{}, false, err
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return EvidenceObservation{}, false, err
	}
	created := rows == 1
	if !created {
		if _, err := tx.ExecContext(ctx, `UPDATE evidence_observations
 SET mirror_message_id = COALESCE(?, mirror_message_id), last_error = ?
 WHERE user_id = ? AND observation_key = ?`, nullInt64(in.MirrorMessageID), in.LastError, in.UserID, in.ObservationKey); err != nil {
			return EvidenceObservation{}, false, err
		}
	}
	obs, err := evidenceObservationByKeyTx(ctx, tx, in.UserID, in.ObservationKey)
	if err != nil {
		return EvidenceObservation{}, false, err
	}
	if created {
		payload, err := json.Marshal(map[string]any{
			"observation_id":      obs.ID,
			"observation_key":     in.ObservationKey,
			"provider":            in.Provider,
			"source_account":      in.SourceAccount,
			"provider_message_id": in.ProviderMessageID,
			"thread_id":           in.ThreadID,
			"folders_json":        in.FoldersJSON,
			"labels_json":         in.LabelsJSON,
			"message_id_header":   in.MessageIDHeader,
			"capture_status":      in.CaptureStatus,
			"raw_sha256":          in.RawSHA256,
			"raw_path":            in.RawPath,
			"raw_size":            in.RawSize,
			"source_size":         in.SourceSize,
			"capture_error":       in.LastError,
		})
		if err != nil {
			return EvidenceObservation{}, false, err
		}
		eventType := "evidence.capture." + in.CaptureStatus
		if _, err := tx.ExecContext(ctx, `INSERT INTO evidence_outbox
 (user_id, observation_id, event_key, event_type, payload, created_at, available_at)
 VALUES (?, ?, ?, ?, ?, ?, ?)`, in.UserID, obs.ID, in.ObservationKey, eventType, string(payload), now, now); err != nil {
			return EvidenceObservation{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return EvidenceObservation{}, false, err
	}
	return obs, created, nil
}

func (s *Store) LinkEvidenceObservation(ctx context.Context, userID int64, observationKey string, mirrorMessageID int64) error {
	if userID <= 0 || strings.TrimSpace(observationKey) == "" || mirrorMessageID <= 0 {
		return errors.New("invalid evidence mirror link")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE evidence_observations
 SET mirror_message_id = (SELECT id FROM messages WHERE user_id = ? AND id = ?)
 WHERE user_id = ? AND observation_key = ?
   AND EXISTS (SELECT 1 FROM messages WHERE user_id = ? AND id = ?)`,
		userID, mirrorMessageID, userID, observationKey, userID, mirrorMessageID)
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

func (s *Store) EvidenceObservations(ctx context.Context, userID int64) ([]EvidenceObservation, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, observation_key, provider, source_account, provider_message_id, thread_id,
 folders_json, labels_json, imap_uid, imap_uid_validity, message_id_header, first_observed_at, source_size, raw_sha256,
 raw_path, raw_size, capture_status, mirror_message_id, last_error
 FROM evidence_observations WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceObservation
	for rows.Next() {
		obs, err := scanEvidenceObservation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

func (s *Store) PendingEvidenceEvents(ctx context.Context, userID int64, limit int, now time.Time) ([]EvidenceEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, user_id, observation_id, event_key, event_type, payload, created_at, available_at, attempts, delivered_at, last_error
 FROM evidence_outbox WHERE user_id = ? AND delivered_at = 0 AND available_at <= ? ORDER BY id LIMIT ?`, userID, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceEvent
	for rows.Next() {
		var event EvidenceEvent
		var created, available, delivered int64
		if err := rows.Scan(&event.ID, &event.UserID, &event.ObservationID, &event.EventKey, &event.EventType, &event.Payload, &created, &available, &event.Attempts, &delivered, &event.LastError); err != nil {
			return nil, err
		}
		event.CreatedAt = unixTime(created)
		event.AvailableAt = unixTime(available)
		event.DeliveredAt = unixTime(delivered)
		out = append(out, event)
	}
	return out, rows.Err()
}

func (s *Store) RetryEvidenceEvent(ctx context.Context, userID, eventID int64, lastError string, availableAt time.Time) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE evidence_outbox SET attempts = attempts + 1, last_error = ?, available_at = ?
 WHERE user_id = ? AND id = ? AND delivered_at = 0`, lastError, availableAt.Unix(), userID, eventID)
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

func (s *Store) DeliverEvidenceEvent(ctx context.Context, userID, eventID int64, deliveredAt time.Time) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	res, err := db.ExecContext(ctx, `UPDATE evidence_outbox SET attempts = attempts + 1, delivered_at = ?, last_error = ''
 WHERE user_id = ? AND id = ? AND delivered_at = 0`, deliveredAt.Unix(), userID, eventID)
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

func validateEvidenceObservationInput(in EvidenceObservationInput) error {
	if in.UserID <= 0 || strings.TrimSpace(in.ObservationKey) == "" || strings.TrimSpace(in.Provider) == "" ||
		strings.TrimSpace(in.SourceAccount) == "" || strings.TrimSpace(in.ProviderMessageID) == "" {
		return errors.New("invalid evidence observation scope")
	}
	if !json.Valid([]byte(in.FoldersJSON)) || !json.Valid([]byte(in.LabelsJSON)) {
		return errors.New("invalid evidence provenance JSON")
	}
	switch in.CaptureStatus {
	case EvidenceCaptureComplete:
		if len(in.RawSHA256) != 64 || in.RawPath == "" || in.RawSize <= 0 || in.LastError != "" {
			return errors.New("invalid complete evidence capture")
		}
	case EvidenceCaptureOversized:
		if in.RawSHA256 != "" || in.RawPath != "" || in.RawSize != 0 {
			return errors.New("oversized evidence cannot claim complete raw")
		}
	case EvidenceCaptureFailed:
		if in.RawSHA256 != "" || in.RawPath != "" || in.RawSize != 0 || strings.TrimSpace(in.LastError) == "" {
			return errors.New("failed evidence capture requires an error and no raw object")
		}
	default:
		return errors.New("invalid evidence capture status")
	}
	return nil
}

func evidenceObservationByKeyTx(ctx context.Context, tx *sql.Tx, userID int64, key string) (EvidenceObservation, error) {
	row := tx.QueryRowContext(ctx, `SELECT id, user_id, observation_key, provider, source_account, provider_message_id, thread_id,
 folders_json, labels_json, imap_uid, imap_uid_validity, message_id_header, first_observed_at, source_size, raw_sha256,
 raw_path, raw_size, capture_status, mirror_message_id, last_error
 FROM evidence_observations WHERE user_id = ? AND observation_key = ?`, userID, key)
	return scanEvidenceObservation(row)
}

type evidenceScanner interface{ Scan(...any) error }

func scanEvidenceObservation(row evidenceScanner) (EvidenceObservation, error) {
	var obs EvidenceObservation
	var uid, uidValidity, mirror sql.NullInt64
	var first int64
	if err := row.Scan(&obs.ID, &obs.UserID, &obs.ObservationKey, &obs.Provider, &obs.SourceAccount, &obs.ProviderMessageID, &obs.ThreadID,
		&obs.FoldersJSON, &obs.LabelsJSON, &uid, &uidValidity, &obs.MessageIDHeader, &first, &obs.SourceSize, &obs.RawSHA256,
		&obs.RawPath, &obs.RawSize, &obs.CaptureStatus, &mirror, &obs.LastError); err != nil {
		return EvidenceObservation{}, err
	}
	obs.IMAPUID = uint32Ptr(uid)
	obs.IMAPUIDValidity = uint32Ptr(uidValidity)
	if mirror.Valid {
		obs.MirrorMessageID = mirror.Int64
	}
	obs.FirstObservedAt = unixTime(first)
	return obs, nil
}

func nullInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullUint32(v *uint32) any {
	if v == nil {
		return nil
	}
	return int64(*v)
}

func uint32Ptr(v sql.NullInt64) *uint32 {
	if !v.Valid || v.Int64 < 0 {
		return nil
	}
	n := uint32(v.Int64)
	return &n
}
