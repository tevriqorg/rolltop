package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProviderSyncGateCensusMustPrecedeApprovalAndNewCensusRelocks(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	user, err := db.CreateUser(ctx, "gate@example.test", "Gate", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ApproveProviderBackfill(ctx, user.ID, "gmail", "google-1"); err == nil {
		t.Fatal("approval succeeded without census")
	}
	g, err := db.SaveProviderCensus(ctx, user.ID, "gmail", "google-1", 123, 1000, 1000, 1500, "metadata only", false)
	if err != nil {
		t.Fatal(err)
	}
	if !g.HasCensus() || g.BackfillApproved() {
		t.Fatalf("gate after census=%+v", g)
	}
	g, err = db.ApproveProviderBackfill(ctx, user.ID, "gmail", "google-1")
	if err != nil {
		t.Fatal(err)
	}
	if !g.BackfillApproved() {
		t.Fatal("approval not persisted")
	}
	g, err = db.SaveProviderCensus(ctx, user.ID, "gmail", "google-1", 124, 1100, 1100, 1650, "metadata only", false)
	if err != nil {
		t.Fatal(err)
	}
	if g.BackfillApproved() {
		t.Fatal("new census did not re-lock backfill")
	}
}
