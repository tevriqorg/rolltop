package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMailAIWorkspacePersistsAcrossStoreRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mail.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "workspace@example.invalid", "Workspace", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	created, err := st.UpsertMailAIWorkspaceItem(ctx, user.ID, MailAIWorkspaceItem{
		Kind: "deadline", Title: "Submit report", Detail: "Due Friday", DueAt: "2026-10-23T17:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Status != "open" {
		t.Fatalf("created=%+v", created)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	items, err := st.ListMailAIWorkspaceItems(ctx, user.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != created.ID || items[0].Title != "Submit report" {
		t.Fatalf("restored items=%+v", items)
	}
}
