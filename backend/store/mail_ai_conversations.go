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

type MailAIMessageSnapshot struct {
	ID      string          `json:"id"`
	Role    string          `json:"role"`
	Payload json.RawMessage `json:"payload"`
}

type MailAIThreadSnapshot struct {
	ID        string            `json:"thread_id"`
	Messages  []json.RawMessage `json:"messages"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
}

func newMailAIThreadID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "mai_" + hex.EncodeToString(b[:]), nil
}

func (s *Store) CurrentMailAIThreadForUser(ctx context.Context, userID int64) (MailAIThreadSnapshot, error) {
	if userID <= 0 {
		return MailAIThreadSnapshot{}, errors.New("mail ai user id is required")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return MailAIThreadSnapshot{}, err
	}
	var out MailAIThreadSnapshot
	var createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, `SELECT id, created_at, updated_at FROM mail_ai_threads
WHERE user_id = ? ORDER BY updated_at DESC, id DESC LIMIT 1`, userID).Scan(&out.ID, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		id, idErr := newMailAIThreadID()
		if idErr != nil {
			return MailAIThreadSnapshot{}, idErr
		}
		now := time.Now().UTC().Unix()
		if _, err = db.ExecContext(ctx, `INSERT INTO mail_ai_threads(user_id, id, created_at, updated_at) VALUES (?, ?, ?, ?)`, userID, id, now, now); err != nil {
			return MailAIThreadSnapshot{}, err
		}
		out.ID, createdAt, updatedAt = id, now, now
	} else if err != nil {
		return MailAIThreadSnapshot{}, err
	}
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
	out.CreatedAt = time.Unix(createdAt, 0).UTC()
	out.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	return out, nil
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
	now := time.Now().UTC().Unix()
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
