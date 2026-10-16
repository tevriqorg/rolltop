package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MailAIThreadScopeGlobal     = "global"
	MailAIThreadScopeMessage    = "message"
	MailAIThreadScopeMailThread = "mail_thread"
)

type MailAIMessageSnapshot struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Payload json.RawMessage `json:"payload"`
}

type MailAIThreadScopeSpec struct {
	Kind    string
	Key     string
	Context json.RawMessage
}

type MailAIThreadSummary struct {
	ID           string          `json:"thread_id"`
	Scope        string          `json:"scope"`
	Context      json.RawMessage `json:"context"`
	Preview      string          `json:"preview"`
	MessageCount int             `json:"message_count"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

type MailAIThreadSnapshot struct {
	ID           string            `json:"thread_id"`
	Scope        string            `json:"scope"`
	Context      json.RawMessage   `json:"context"`
	Preview      string            `json:"preview"`
	MessageCount int               `json:"message_count"`
	Messages     []json.RawMessage `json:"messages"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

func newMailAIThreadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "mai_" + hex.EncodeToString(b[:]), nil
}

func mailAINow() int64 { return time.Now().UTC().UnixMilli() }

func mailAITime(value int64) time.Time {
	// A1 stored seconds. A2 writes milliseconds so rapid thread creation and
	// switching have a deterministic recency order without rewriting old rows.
	if value > 100_000_000_000 {
		return time.UnixMilli(value).UTC()
	}
	return time.Unix(value, 0).UTC()
}

func normalizeMailAIThreadScope(spec MailAIThreadScopeSpec) (MailAIThreadScopeSpec, error) {
	spec.Kind = strings.TrimSpace(spec.Kind)
	spec.Key = strings.TrimSpace(spec.Key)
	switch spec.Kind {
	case MailAIThreadScopeGlobal, MailAIThreadScopeMessage, MailAIThreadScopeMailThread:
	default:
		return MailAIThreadScopeSpec{}, fmt.Errorf("invalid mail ai thread scope %q", spec.Kind)
	}
	if spec.Key == "" {
		return MailAIThreadScopeSpec{}, errors.New("mail ai thread scope key is required")
	}
	if len(spec.Context) == 0 {
		spec.Context = json.RawMessage(`{}`)
	}
	if !json.Valid(spec.Context) || spec.Context[0] != '{' {
		return MailAIThreadScopeSpec{}, errors.New("mail ai thread context must be a json object")
	}
	return spec, nil
}

func (s *Store) CreateMailAIThreadForUser(ctx context.Context, userID int64, spec MailAIThreadScopeSpec) (MailAIThreadSnapshot, error) {
	if userID <= 0 {
		return MailAIThreadSnapshot{}, errors.New("mail ai user id is required")
	}
	spec, err := normalizeMailAIThreadScope(spec)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	id, err := newMailAIThreadID()
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	now := mailAINow()
	if _, err := db.ExecContext(ctx, `INSERT INTO mail_ai_threads
(user_id, id, created_at, updated_at, scope_kind, scope_key, context_json)
VALUES (?, ?, ?, ?, ?, ?, ?)`, userID, id, now, now, spec.Kind, spec.Key, string(spec.Context)); err != nil {
		return MailAIThreadSnapshot{}, err
	}
	return MailAIThreadSnapshot{
		ID: id, Scope: spec.Kind, Context: append(json.RawMessage(nil), spec.Context...),
		Messages: []json.RawMessage{}, CreatedAt: mailAITime(now), UpdatedAt: mailAITime(now),
	}, nil
}

func (s *Store) CurrentMailAIThreadForScopeForUser(ctx context.Context, userID int64, spec MailAIThreadScopeSpec) (MailAIThreadSnapshot, error) {
	if userID <= 0 {
		return MailAIThreadSnapshot{}, errors.New("mail ai user id is required")
	}
	spec, err := normalizeMailAIThreadScope(spec)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	var id string
	err = db.QueryRowContext(ctx, `SELECT id FROM mail_ai_threads
WHERE user_id = ? AND scope_kind = ? AND scope_key = ?
ORDER BY updated_at DESC, id DESC LIMIT 1`, userID, spec.Kind, spec.Key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return s.CreateMailAIThreadForUser(ctx, userID, spec)
	}
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	return s.MailAIThreadForUser(ctx, userID, id)
}

func (s *Store) CurrentMailAIThreadForUser(ctx context.Context, userID int64) (MailAIThreadSnapshot, error) {
	return s.CurrentMailAIThreadForScopeForUser(ctx, userID, MailAIThreadScopeSpec{
		Kind: MailAIThreadScopeGlobal, Key: MailAIThreadScopeGlobal, Context: json.RawMessage(`{"scope":"global"}`),
	})
}

func (s *Store) MailAIThreadForUser(ctx context.Context, userID int64, threadID string) (MailAIThreadSnapshot, error) {
	threadID = strings.TrimSpace(threadID)
	if userID <= 0 || threadID == "" {
		return MailAIThreadSnapshot{}, errors.New("mail ai user id and thread id are required")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	var out MailAIThreadSnapshot
	var contextJSON string
	var createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, `SELECT id, scope_kind, context_json, created_at, updated_at
FROM mail_ai_threads WHERE user_id = ? AND id = ?`, userID, threadID).
		Scan(&out.ID, &out.Scope, &contextJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return MailAIThreadSnapshot{}, ErrNotFound
	}
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	if !json.Valid([]byte(contextJSON)) {
		return MailAIThreadSnapshot{}, errors.New("stored mail ai thread context is invalid")
	}
	out.Context = json.RawMessage(contextJSON)
	rows, err := db.QueryContext(ctx, `SELECT payload_json FROM mail_ai_messages
WHERE user_id = ? AND thread_id = ? ORDER BY position`, userID, out.ID)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	defer rows.Close()
	out.Messages = make([]json.RawMessage, 0)
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return MailAIThreadSnapshot{}, err
		}
		out.Messages = append(out.Messages, json.RawMessage(payload))
	}
	if err := rows.Err(); err != nil {
		return MailAIThreadSnapshot{}, err
	}
	out.MessageCount = len(out.Messages)
	out.Preview = mailAIThreadPreview(out.Messages)
	out.CreatedAt = mailAITime(createdAt)
	out.UpdatedAt = mailAITime(updatedAt)
	return out, nil
}

func (s *Store) ListMailAIThreadsForUser(ctx context.Context, userID int64, limit int) ([]MailAIThreadSummary, error) {
	if userID <= 0 {
		return nil, errors.New("mail ai user id is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT t.id, t.scope_kind, t.context_json, t.created_at, t.updated_at,
 (SELECT COUNT(*) FROM mail_ai_messages m WHERE m.user_id = t.user_id AND m.thread_id = t.id),
 COALESCE((SELECT m.payload_json FROM mail_ai_messages m
   WHERE m.user_id = t.user_id AND m.thread_id = t.id AND m.role = 'user'
   ORDER BY m.position LIMIT 1), '')
FROM mail_ai_threads t WHERE t.user_id = ?
ORDER BY t.updated_at DESC, t.id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]MailAIThreadSummary, 0)
	for rows.Next() {
		var item MailAIThreadSummary
		var contextJSON, previewPayload string
		var createdAt, updatedAt int64
		if err := rows.Scan(&item.ID, &item.Scope, &contextJSON, &createdAt, &updatedAt, &item.MessageCount, &previewPayload); err != nil {
			return nil, err
		}
		if !json.Valid([]byte(contextJSON)) {
			return nil, errors.New("stored mail ai thread context is invalid")
		}
		item.Context = json.RawMessage(contextJSON)
		if previewPayload != "" {
			item.Preview = mailAIMessagePreview(json.RawMessage(previewPayload))
		}
		item.CreatedAt = mailAITime(createdAt)
		item.UpdatedAt = mailAITime(updatedAt)
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func mailAIThreadPreview(messages []json.RawMessage) string {
	for _, raw := range messages {
		var header struct {
			Role string `json:"role"`
		}
		if json.Unmarshal(raw, &header) == nil && header.Role == "user" {
			if preview := mailAIMessagePreview(raw); preview != "" {
				return preview
			}
		}
	}
	return ""
}

func mailAIMessagePreview(raw json.RawMessage) string {
	var message struct {
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.Unmarshal(raw, &message) != nil {
		return ""
	}
	for _, part := range message.Parts {
		if part.Type != "text" {
			continue
		}
		preview := strings.Join(strings.Fields(part.Text), " ")
		runes := []rune(preview)
		if len(runes) > 96 {
			preview = string(runes[:96]) + "…"
		}
		return preview
	}
	return ""
}

func (s *Store) SaveMailAIThreadMessagesForUser(ctx context.Context, userID int64, threadID string, messages []MailAIMessageSnapshot) error {
	threadID = strings.TrimSpace(threadID)
	if userID <= 0 || threadID == "" {
		return errors.New("mail ai user id and thread id are required")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owned int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM mail_ai_threads WHERE user_id = ? AND id = ?`, userID, threadID).Scan(&owned); err != nil {
		return err
	}
	if owned != 1 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM mail_ai_messages WHERE user_id = ? AND thread_id = ?`, userID, threadID); err != nil {
		return err
	}
	now := mailAINow()
	seen := make(map[string]struct{}, len(messages))
	for position, message := range messages {
		id := strings.TrimSpace(message.ID)
		role := strings.TrimSpace(message.Role)
		if id == "" || role == "" || !json.Valid(message.Payload) {
			return fmt.Errorf("invalid mail ai message at position %d", position)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("duplicate mail ai message id %q", id)
		}
		seen[id] = struct{}{}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mail_ai_messages
(user_id, thread_id, position, message_id, role, payload_json, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			userID, threadID, position, id, role, string(message.Payload), now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE mail_ai_threads SET updated_at = ? WHERE user_id = ? AND id = ?`, now, userID, threadID); err != nil {
		return err
	}
	return tx.Commit()
}
