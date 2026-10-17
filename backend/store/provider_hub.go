package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

type ProviderSource struct {
	Provider             string
	ID                   string
	AccountID, MailboxID int64
	Cursor               string
}

// EnsureProviderSource creates a provider-managed compatibility envelope for
// the existing Rolltop message/blob/index stores. Provider state stays native
// in provider_sources; the compatibility account is intentionally excluded
// from IMAP scheduling and account settings.
func (s *Store) EnsureProviderSource(ctx context.Context, userID int64, providerName, id, label, email string) (ProviderSource, error) {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	id = strings.TrimSpace(id)
	label = strings.TrimSpace(label)
	email = cleanEmail(email)
	if userID <= 0 || providerName == "" || id == "" || label == "" || email == "" {
		return ProviderSource{}, errors.New("provider source requires tenant, provider, id, label, and email")
	}
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderSource{}, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return ProviderSource{}, err
	}
	defer tx.Rollback()
	out := ProviderSource{Provider: providerName, ID: id}
	err = tx.QueryRowContext(ctx, `SELECT account_id,mailbox_id,cursor FROM provider_sources WHERE user_id=? AND provider=? AND source_id=?`, userID, providerName, id).Scan(&out.AccountID, &out.MailboxID, &out.Cursor)
	if err == sql.ErrNoRows {
		result, e := tx.ExecContext(ctx, `INSERT INTO mail_accounts(user_id,email,label,host,port,username,encrypted_password,created_at,updated_at) VALUES(?,?,?,'',0,'','',?,?)`, userID, email, label, nowUnix(), nowUnix())
		if e != nil {
			return out, e
		}
		out.AccountID, err = result.LastInsertId()
		if err != nil {
			return out, err
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO mailboxes(user_id,account_id,name,sync_mode,role,icon,show_in_sidebar,show_in_all_mail,include_in_search,created_at,updated_at) VALUES(?,?,'INBOX','off','inbox','inbox',1,1,1,?,?)`, userID, out.AccountID, nowUnix(), nowUnix())
		if err != nil {
			return out, err
		}
		out.MailboxID, err = result.LastInsertId()
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_sources(user_id,provider,source_id,account_id,mailbox_id) VALUES(?,?,?,?,?)`, userID, providerName, id, out.AccountID, out.MailboxID)
	}
	if err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mail_accounts SET email=?,label=?,updated_at=? WHERE user_id=? AND id=?`, email, label, nowUnix(), userID, out.AccountID); err != nil {
		return out, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE mailboxes SET name='INBOX',role='inbox',icon='inbox',sync_mode='off',show_in_sidebar=1,show_in_all_mail=1,include_in_search=1,updated_at=? WHERE user_id=? AND id=?`, nowUnix(), userID, out.MailboxID); err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// EnsureMockSource preserves the Gate-F API while using the provider-neutral store.
func (s *Store) EnsureMockSource(ctx context.Context, userID int64, id string) (ProviderSource, error) {
	label, email, ok := mockSourceIdentity(id)
	if !ok {
		return ProviderSource{}, errors.New("unknown mock account")
	}
	return s.EnsureProviderSource(ctx, userID, "mock", id, label, email)
}

func mockSourceIdentity(id string) (label, email string, ok bool) {
	switch id {
	case "personal":
		return "Personal", "vipdonmen.personal@example.test", true
	case "work":
		return "Work", "vipdonmen.work@example.test", true
	case "travel":
		return "Travel", "vipdonmen.travel@example.test", true
	default:
		return "", "", false
	}
}

func (s *Store) GetProviderSource(ctx context.Context, userID int64, providerName, id string) (ProviderSource, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderSource{}, err
	}
	out := ProviderSource{Provider: strings.ToLower(strings.TrimSpace(providerName)), ID: strings.TrimSpace(id)}
	err = db.QueryRowContext(ctx, `SELECT account_id,mailbox_id,cursor FROM provider_sources WHERE user_id=? AND provider=? AND source_id=?`, userID, out.Provider, out.ID).Scan(&out.AccountID, &out.MailboxID, &out.Cursor)
	if err == sql.ErrNoRows {
		return ProviderSource{}, ErrNotFound
	}
	return out, err
}

func (s *Store) ProviderJournal(ctx context.Context, userID int64, id string) ([]string, error) {
	return s.ProviderJournalFor(ctx, userID, "mock", id)
}
func (s *Store) ProviderJournalFor(ctx context.Context, userID int64, providerName, id string) ([]string, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT mutation FROM provider_mutations WHERE user_id=? AND provider=? AND source_id=? ORDER BY id`, userID, strings.ToLower(strings.TrimSpace(providerName)), id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
func (s *Store) AppendProviderMutation(ctx context.Context, userID int64, id, mutation string) error {
	return s.AppendProviderMutationFor(ctx, userID, "mock", id, mutation)
}
func (s *Store) AppendProviderMutationFor(ctx context.Context, userID int64, providerName, id, mutation string) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO provider_mutations(user_id,provider,source_id,mutation) VALUES(?,?,?,?)`, userID, strings.ToLower(strings.TrimSpace(providerName)), id, mutation)
	return err
}
func (s *Store) SetProviderCursor(ctx context.Context, userID int64, id, cursor string) error {
	return s.SetProviderCursorFor(ctx, userID, "mock", id, cursor)
}
func (s *Store) SetProviderCursorFor(ctx context.Context, userID int64, providerName, id, cursor string) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE provider_sources SET cursor=? WHERE user_id=? AND provider=? AND source_id=?`, cursor, userID, strings.ToLower(strings.TrimSpace(providerName)), id)
	if err != nil {
		return err
	}
	if n, err := result.RowsAffected(); err != nil {
		return err
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

type ProviderMessage struct {
	ID        int64  `json:"id"`
	Provider  string `json:"provider"`
	Account   string `json:"account"`
	NativeID  string `json:"native_id"`
	ThreadID  string `json:"thread_id"`
	Folders   string `json:"-"`
	Labels    string `json:"-"`
	Oversized bool   `json:"oversized"`
}

func (s *Store) ProviderMessages(ctx context.Context, userID int64) ([]ProviderMessage, error) {
	return s.ProviderMessagesFor(ctx, userID, "mock")
}
func (s *Store) ProviderMessagesFor(ctx context.Context, userID int64, providerName string) ([]ProviderMessage, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	query := `SELECT message_id,provider,source_id,native_id,thread_id,folders,labels,oversized FROM provider_messages WHERE user_id=?`
	args := []any{userID}
	if providerName != "" {
		query += ` AND provider=?`
		args = append(args, providerName)
	}
	query += ` ORDER BY message_id`
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderMessage{}
	for rows.Next() {
		var m ProviderMessage
		if err := rows.Scan(&m.ID, &m.Provider, &m.Account, &m.NativeID, &m.ThreadID, &m.Folders, &m.Labels, &m.Oversized); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) ProviderMessageForNativeID(ctx context.Context, userID int64, providerName, sourceID, nativeID string) (ProviderMessage, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderMessage{}, err
	}
	var m ProviderMessage
	err = db.QueryRowContext(ctx, `SELECT message_id,provider,source_id,native_id,thread_id,folders,labels,oversized FROM provider_messages WHERE user_id=? AND provider=? AND source_id=? AND native_id=?`, userID, strings.ToLower(strings.TrimSpace(providerName)), sourceID, nativeID).Scan(&m.ID, &m.Provider, &m.Account, &m.NativeID, &m.ThreadID, &m.Folders, &m.Labels, &m.Oversized)
	if err == sql.ErrNoRows {
		return ProviderMessage{}, ErrNotFound
	}
	return m, err
}

func (s *Store) ProviderMessageForLocalID(ctx context.Context, userID, messageID int64) (ProviderMessage, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return ProviderMessage{}, err
	}
	var m ProviderMessage
	err = db.QueryRowContext(ctx, `SELECT message_id,provider,source_id,native_id,thread_id,folders,labels,oversized FROM provider_messages WHERE user_id=? AND message_id=?`, userID, messageID).Scan(&m.ID, &m.Provider, &m.Account, &m.NativeID, &m.ThreadID, &m.Folders, &m.Labels, &m.Oversized)
	if err == sql.ErrNoRows {
		return ProviderMessage{}, ErrNotFound
	}
	return m, err
}

func (s *Store) SaveProviderMessage(ctx context.Context, userID int64, m ProviderMessage, read, star bool) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	providerName := strings.ToLower(strings.TrimSpace(m.Provider))
	if providerName == "" {
		providerName = "mock"
	}
	if m.ID <= 0 || strings.TrimSpace(m.Account) == "" || strings.TrimSpace(m.NativeID) == "" {
		return errors.New("provider message requires local id, account, and native id")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_messages(user_id,provider,source_id,native_id,message_id,thread_id,folders,labels,oversized) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(user_id,provider,source_id,native_id) DO UPDATE SET message_id=excluded.message_id,thread_id=excluded.thread_id,folders=excluded.folders,labels=excluded.labels,oversized=excluded.oversized`, userID, providerName, m.Account, m.NativeID, m.ID, m.ThreadID, m.Folders, m.Labels, m.Oversized)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE messages SET is_read=?,is_starred=?,read_sync_pending=0,star_sync_pending=0,thread_key=? WHERE user_id=? AND id=?`, read, star, fmt.Sprintf("%s:%d:%s:%s", providerName, userID, m.Account, m.ThreadID), userID, m.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteProviderMessage removes only the mutable Mirror row. Evidence remains
// independent and is intentionally not deleted by a source-side deletion.
func (s *Store) DeleteProviderMessage(ctx context.Context, userID int64, providerName, sourceID, nativeID string) (int64, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return 0, err
	}
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	var messageID int64
	err = db.QueryRowContext(ctx, `SELECT message_id FROM provider_messages WHERE user_id=? AND provider=? AND source_id=? AND native_id=?`, userID, providerName, sourceID, nativeID).Scan(&messageID)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if err := s.DeleteMessageForUser(ctx, userID, messageID); err != nil && !IsNotFound(err) {
		return 0, err
	}
	return messageID, nil
}
