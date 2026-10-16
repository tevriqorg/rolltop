package store

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type AccountConnection struct {
	ID                    int64
	UserID                int64
	Provider              string
	ExternalSubject       string
	Email                 string
	DisplayName           string
	EncryptedRefreshToken string
	Scopes                []string
	Capabilities          []string
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (s *Store) UpsertAccountConnection(ctx context.Context, userID int64, in AccountConnection) (AccountConnection, error) {
	in.UserID = userID
	if err := validateAccountConnection(in); err != nil {
		return AccountConnection{}, err
	}
	scopes, _ := json.Marshal(normalizeStringSet(in.Scopes))
	capabilities, _ := json.Marshal(normalizeStringSet(in.Capabilities))
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return AccountConnection{}, err
	}
	now := nowUnix()
	_, err = db.ExecContext(ctx, `INSERT INTO account_connections
		(user_id,provider,external_subject,email,display_name,encrypted_refresh_token,scopes_json,capabilities_json,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(user_id,provider,external_subject) DO UPDATE SET
			email=excluded.email,
			display_name=excluded.display_name,
			encrypted_refresh_token=excluded.encrypted_refresh_token,
			scopes_json=excluded.scopes_json,
			capabilities_json=excluded.capabilities_json,
			updated_at=excluded.updated_at`,
		userID, strings.ToLower(strings.TrimSpace(in.Provider)), strings.TrimSpace(in.ExternalSubject), cleanEmail(in.Email),
		strings.TrimSpace(in.DisplayName), in.EncryptedRefreshToken, string(scopes), string(capabilities), now, now)
	if err != nil {
		return AccountConnection{}, err
	}
	return s.GetAccountConnection(ctx, userID, in.Provider, in.ExternalSubject)
}

func (s *Store) GetAccountConnection(ctx context.Context, userID int64, provider, externalSubject string) (AccountConnection, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return AccountConnection{}, err
	}
	row := db.QueryRowContext(ctx, `SELECT id,user_id,provider,external_subject,email,display_name,encrypted_refresh_token,scopes_json,capabilities_json,created_at,updated_at
		FROM account_connections WHERE user_id=? AND provider=? AND external_subject=?`,
		userID, strings.ToLower(strings.TrimSpace(provider)), strings.TrimSpace(externalSubject))
	return scanAccountConnection(row)
}

type accountConnectionRow interface {
	Scan(...any) error
}

func scanAccountConnection(row accountConnectionRow) (AccountConnection, error) {
	var out AccountConnection
	var scopesJSON, capabilitiesJSON string
	var created, updated int64
	if err := row.Scan(&out.ID, &out.UserID, &out.Provider, &out.ExternalSubject, &out.Email, &out.DisplayName,
		&out.EncryptedRefreshToken, &scopesJSON, &capabilitiesJSON, &created, &updated); err != nil {
		return AccountConnection{}, err
	}
	if err := json.Unmarshal([]byte(scopesJSON), &out.Scopes); err != nil {
		return AccountConnection{}, err
	}
	if err := json.Unmarshal([]byte(capabilitiesJSON), &out.Capabilities); err != nil {
		return AccountConnection{}, err
	}
	out.CreatedAt = unixTime(created)
	out.UpdatedAt = unixTime(updated)
	return out, nil
}

func validateAccountConnection(in AccountConnection) error {
	if in.UserID <= 0 || strings.TrimSpace(in.Provider) == "" || strings.TrimSpace(in.ExternalSubject) == "" || cleanEmail(in.Email) == "" {
		return errors.New("account connection requires tenant, provider, subject, and email")
	}
	if !strings.HasPrefix(in.EncryptedRefreshToken, "v1:") {
		return errors.New("account connection refresh token must be encrypted")
	}
	return nil
}
func normalizeStringSet(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (s *Store) ListAccountConnections(ctx context.Context, userID int64, provider string) ([]AccountConnection, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id,user_id,provider,external_subject,email,display_name,encrypted_refresh_token,scopes_json,capabilities_json,created_at,updated_at
		FROM account_connections WHERE user_id=? AND provider=? ORDER BY email,external_subject`, userID, strings.ToLower(strings.TrimSpace(provider)))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AccountConnection{}
	for rows.Next() {
		connection, err := scanAccountConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, connection)
	}
	return out, rows.Err()
}
