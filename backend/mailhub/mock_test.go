package mailhub

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestMockIngestionMutationRestartAndTenantIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "system.db")
	st, err := store.OpenServer(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	index, err := search.OpenPerUser(filepath.Join(dir, "indexes"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { st.Close(); index.Close() }()
	owner, err := st.CreateUser(ctx, "mock-owner@example.invalid", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "mock-other@example.invalid", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	hub := Service{st, blob.New(dir), index}
	for _, user := range []int64{owner.ID, other.ID} {
		for _, a := range provider.MockAccounts(user) {
			if err := hub.Sync(ctx, a); err != nil {
				t.Fatal(err)
			}
		}
	}
	all, err := hub.Messages(ctx, owner.ID, Filter{})
	if err != nil || len(all) != 6 {
		t.Fatalf("canonical ingestion: %d %v", len(all), err)
	}
	for _, a := range provider.MockAccounts(owner.ID) {
		if err := hub.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	repeated, err := hub.Messages(ctx, owner.ID, Filter{})
	if err != nil || len(repeated) != 6 {
		t.Fatal("sync duplicated canonical messages")
	}
	for _, test := range []struct {
		f Filter
		n int
	}{{Filter{View: "inbox"}, 4}, {Filter{View: "unread"}, 3}, {Filter{View: "starred"}, 4}, {Filter{Account: "work"}, 2}, {Filter{Label: "Projects"}, 1}, {Filter{Label: "项目"}, 3}, {Filter{Provider: "imap"}, 0}, {Filter{Query: "notes.txt"}, 2}, {Filter{Query: "固定附件"}, 2}, {Filter{Query: "项目计划"}, 3}, {Filter{Query: "確認しました"}, 1}} {
		got, err := hub.Messages(ctx, owner.ID, test.f)
		if err != nil || len(got) != test.n {
			t.Fatalf("filter %+v = %d, want %d: %v", test.f, len(got), test.n, err)
		}
	}
	meta, err := st.ProviderMessages(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	source, err := st.EnsureMockSource(ctx, owner.ID, "personal")
	if err != nil {
		t.Fatal(err)
	}
	if accounts, err := st.ListAccounts(ctx); err != nil || len(accounts) != 0 {
		t.Fatalf("mock entered IMAP scheduling: %+v %v", accounts, err)
	}
	if _, err := st.GetMailAccountForUser(ctx, owner.ID, source.AccountID); err == nil {
		t.Fatal("mock usable as an IMAP account")
	}
	db, err := st.UserDB(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	var credentials, smtp, attachmentBlobs int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM mail_accounts WHERE encrypted_password<>'' OR encrypted_smtp_password<>''`).Scan(&credentials); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM smtp_accounts`).Scan(&smtp); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM attachments WHERE blob_path<>''`).Scan(&attachmentBlobs); err != nil {
		t.Fatal(err)
	}
	if credentials != 0 || smtp != 0 || attachmentBlobs != 0 {
		t.Fatal("credentials, SMTP, or attachment bodies were created")
	}
	for _, m := range meta {
		if m.Account == "personal" && m.NativeID == "planning" {
			if _, err := st.GetMessageForUser(ctx, other.ID, m.ID); err == nil {
				// Per-user SQLite IDs can overlap. Compare source tenant, never assume a
				// globally unique integer across the split stores.
				row, err := st.GetMessageForUser(ctx, other.ID, m.ID)
				if err != nil || row.UserID != other.ID {
					t.Fatal("message crossed tenants")
				}
			}
		}
	}
	a := provider.MockAccounts(owner.ID)[0]
	for _, change := range []provider.Mutation{{MessageID: "planning", Action: "read", Value: true}, {MessageID: "planning", Action: "star", Value: false}, {MessageID: "planning", Action: "label", Value: true, Label: "verified"}, {MessageID: "planning", Action: "archive"}, {MessageID: "attachment", Action: "trash"}} {
		if err := hub.Mutate(ctx, a, change); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	index.Close()
	st, err = store.OpenServer(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	index, err = search.OpenPerUser(filepath.Join(dir, "indexes"))
	if err != nil {
		t.Fatal(err)
	}
	hub = Service{st, blob.New(dir), index}
	p, err := hub.Source(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot[0].Read || snapshot[0].Starred || slices.Contains(snapshot[0].Folders, "INBOX") || !slices.Contains(snapshot[0].Labels, "verified") {
		t.Fatal("source mutations lost after restart")
	}
	if err := hub.Sync(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := hub.Messages(ctx, owner.ID, Filter{Label: "verified"})
	if err != nil || len(got) != 1 || !got[0].Read || got[0].Starred {
		t.Fatalf("restart mirror: %+v %v", got, err)
	}
	got, err = hub.Messages(ctx, owner.ID, Filter{View: "trash"})
	if err != nil || len(got) != 1 || len(got[0].Attachments) != 1 {
		t.Fatal("trash/attachment projection lost")
	}
	got, err = hub.Messages(ctx, other.ID, Filter{Label: "verified"})
	if err != nil || len(got) != 0 {
		t.Fatal("mutation leaked across tenants")
	}
	// Force checkpoint recovery to exercise idempotent projection, not only the
	// unchanged-cursor fast path.
	if err := st.SetProviderCursor(ctx, owner.ID, "personal", ""); err != nil {
		t.Fatal(err)
	}
	if err := hub.Sync(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err = hub.Messages(ctx, owner.ID, Filter{})
	if err != nil || len(got) != 5 {
		t.Fatalf("recovery duplicated mail: %d %v", len(got), err)
	}
}
