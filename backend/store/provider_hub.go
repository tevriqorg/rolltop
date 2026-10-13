package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type ProviderSource struct {
	ID                   string
	AccountID, MailboxID int64
	Cursor               string
}

// EnsureMockSource creates compatibility envelopes for existing message/blob
// tables, never IMAP accounts usable by account lookup or scheduling.
func (s *Store) EnsureMockSource(ctx context.Context, userID int64, id string) (ProviderSource, error) {
	if userID <= 0 || (id != "personal" && id != "work") {
		return ProviderSource{}, errors.New("unknown mock account")
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
	out := ProviderSource{ID: id}
	err = tx.QueryRowContext(ctx, `SELECT account_id,mailbox_id,cursor FROM provider_sources WHERE user_id=? AND provider='mock' AND source_id=?`, userID, id).Scan(&out.AccountID, &out.MailboxID, &out.Cursor)
	if err == sql.ErrNoRows {
		result, e := tx.ExecContext(ctx, `INSERT INTO mail_accounts(user_id,email,label,host,port,username,encrypted_password,created_at,updated_at) VALUES(?,?,?,'',0,'','',?,?)`, userID, id+"@example.invalid", "Mock "+id, nowUnix(), nowUnix())
		if e != nil {
			return out, e
		}
		out.AccountID, err = result.LastInsertId()
		if err != nil {
			return out, err
		}
		result, err = tx.ExecContext(ctx, `INSERT INTO mailboxes(user_id,account_id,name,sync_mode,show_in_sidebar,show_in_all_mail,include_in_search,created_at,updated_at) VALUES(?,?,'Mock mirror','off',0,0,1,?,?)`, userID, out.AccountID, nowUnix(), nowUnix())
		if err != nil {
			return out, err
		}
		out.MailboxID, err = result.LastInsertId()
		if err != nil {
			return out, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO provider_sources(user_id,provider,source_id,account_id,mailbox_id) VALUES(?,'mock',?,?,?)`, userID, id, out.AccountID, out.MailboxID)
	}
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}
func (s *Store) ProviderJournal(ctx context.Context, userID int64, id string) ([]string, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT mutation FROM provider_mutations WHERE user_id=? AND provider='mock' AND source_id=? ORDER BY id`, userID, id)
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
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `INSERT INTO provider_mutations(user_id,provider,source_id,mutation) VALUES(?,'mock',?,?)`, userID, id, mutation)
	return err
}
func (s *Store) SetProviderCursor(ctx context.Context, userID int64, id, cursor string) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE provider_sources SET cursor=? WHERE user_id=? AND provider='mock' AND source_id=?`, cursor, userID, id)
	return err
}

type ProviderMessage struct {
	ID        int64  `json:"id"`
	Account   string `json:"account"`
	NativeID  string `json:"native_id"`
	ThreadID  string `json:"thread_id"`
	Folders   string `json:"-"`
	Labels    string `json:"-"`
	Oversized bool   `json:"oversized"`
}

func (s *Store) ProviderMessages(ctx context.Context, userID int64) ([]ProviderMessage, error) {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT message_id,source_id,native_id,thread_id,folders,labels,oversized FROM provider_messages WHERE user_id=? AND provider='mock' ORDER BY message_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderMessage{}
	for rows.Next() {
		var m ProviderMessage
		if err := rows.Scan(&m.ID, &m.Account, &m.NativeID, &m.ThreadID, &m.Folders, &m.Labels, &m.Oversized); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) SaveProviderMessage(ctx context.Context, userID int64, m ProviderMessage, read, star bool) error {
	db, err := s.dataDB(ctx, userID)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO provider_messages(user_id,provider,source_id,native_id,message_id,thread_id,folders,labels,oversized) VALUES(?,'mock',?,?,?,?,?,?,?) ON CONFLICT(user_id,provider,source_id,native_id) DO UPDATE SET folders=excluded.folders,labels=excluded.labels`, userID, m.Account, m.NativeID, m.ID, m.ThreadID, m.Folders, m.Labels, m.Oversized)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE messages SET is_read=?,is_starred=?,read_sync_pending=0,star_sync_pending=0,thread_key=? WHERE user_id=? AND id=?`, read, star, fmt.Sprintf("mock:%d:%s:%s", userID, m.Account, m.ThreadID), userID, m.ID)
	if err != nil {
		return err
	}
	return tx.Commit()
}
