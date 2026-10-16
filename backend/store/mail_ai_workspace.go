package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type MailAIWorkspaceItem struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Title     string    `json:"title"`
	Detail    string    `json:"detail"`
	MessageID int64     `json:"message_id,omitempty"`
	Status    string    `json:"status"`
	DueAt     string    `json:"due_at,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func normalizeMailAIWorkspaceKind(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "reply", "deadline", "waiting", "risk":
		return value, nil
	default:
		return "", fmt.Errorf("invalid workspace item kind")
	}
}
func normalizeMailAIWorkspaceStatus(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = "open"
	}
	switch value {
	case "open", "done":
		return value, nil
	default:
		return "", fmt.Errorf("invalid workspace item status")
	}
}

func (s *Store) UpsertMailAIWorkspaceItem(ctx context.Context, userID int64, item MailAIWorkspaceItem) (MailAIWorkspaceItem, error) {
	kind, err := normalizeMailAIWorkspaceKind(item.Kind)
	if err != nil {
		return MailAIWorkspaceItem{}, err
	}
	status, err := normalizeMailAIWorkspaceStatus(item.Status)
	if err != nil {
		return MailAIWorkspaceItem{}, err
	}
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" || len(item.Title) > 240 || len(item.Detail) > 4000 || len(item.DueAt) > 80 {
		return MailAIWorkspaceItem{}, fmt.Errorf("invalid workspace item")
	}
	if strings.TrimSpace(item.ID) == "" {
		item.ID, err = newMailAIThreadID()
		if err != nil {
			return MailAIWorkspaceItem{}, err
		}
	}
	now := nowUnix()
	_, err = s.mustDataDB(ctx, userID).ExecContext(ctx, `INSERT INTO mail_ai_workspace_items
 (user_id, id, kind, title, detail, message_id, status, due_at, created_at, updated_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT(user_id, id) DO UPDATE SET
 kind=excluded.kind, title=excluded.title, detail=excluded.detail,
 message_id=excluded.message_id, status=excluded.status, due_at=excluded.due_at,
 updated_at=excluded.updated_at`,
		userID, item.ID, kind, item.Title, item.Detail, item.MessageID, status, strings.TrimSpace(item.DueAt), now, now)
	if err != nil {
		return MailAIWorkspaceItem{}, err
	}
	return s.GetMailAIWorkspaceItem(ctx, userID, item.ID)
}

func (s *Store) GetMailAIWorkspaceItem(ctx context.Context, userID int64, id string) (MailAIWorkspaceItem, error) {
	var item MailAIWorkspaceItem
	var created, updated int64
	err := s.mustDataDB(ctx, userID).QueryRowContext(ctx, `SELECT id, kind, title, detail, message_id, status, due_at, created_at, updated_at
 FROM mail_ai_workspace_items WHERE user_id=? AND id=?`, userID, strings.TrimSpace(id)).
		Scan(&item.ID, &item.Kind, &item.Title, &item.Detail, &item.MessageID, &item.Status, &item.DueAt, &created, &updated)
	item.CreatedAt = unixTime(created)
	item.UpdatedAt = unixTime(updated)
	return item, err
}
func (s *Store) ListMailAIWorkspaceItems(ctx context.Context, userID int64, limit int) ([]MailAIWorkspaceItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := s.mustDataDB(ctx, userID).QueryContext(ctx, `SELECT id, kind, title, detail, message_id, status, due_at, created_at, updated_at
 FROM mail_ai_workspace_items WHERE user_id=?
 ORDER BY CASE status WHEN 'open' THEN 0 ELSE 1 END, updated_at DESC, id DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]MailAIWorkspaceItem, 0)
	for rows.Next() {
		var item MailAIWorkspaceItem
		var created, updated int64
		if err := rows.Scan(&item.ID, &item.Kind, &item.Title, &item.Detail, &item.MessageID, &item.Status, &item.DueAt, &created, &updated); err != nil {
			return nil, err
		}
		item.CreatedAt = unixTime(created)
		item.UpdatedAt = unixTime(updated)
		items = append(items, item)
	}
	return items, rows.Err()
}
