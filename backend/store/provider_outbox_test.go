package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestProviderOutboxIdempotencyAndCrashRecovery(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.CreateUser(ctx, "outbox@example.test", "Outbox", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateBlob(ctx, BlobRecord{UserID: u.ID, Kind: "outbox-message", Path: "x", SHA256: "abc", Size: 3})
	if err != nil {
		t.Fatal(err)
	}
	in := ProviderOutboxEnqueue{UserID: u.ID, Provider: "gmail", SourceID: "google-1", SubmissionKey: "stable", ThreadID: "gmail-thread-1", BlobID: b.ID, RawSHA256: "abc", RawSize: 3}
	first, created, err := st.EnqueueProviderOutbox(ctx, in)
	if err != nil || !created || first.ThreadID != "gmail-thread-1" {
		t.Fatalf("enqueue=%+v created=%v err=%v", first, created, err)
	}
	again, created, err := st.EnqueueProviderOutbox(ctx, in)
	if err != nil || created || again.ID != first.ID || again.ThreadID != "gmail-thread-1" {
		t.Fatalf("idempotency first=%+v again=%+v created=%v err=%v", first, again, created, err)
	}
	claimed, ok, err := st.ClaimNextProviderOutbox(ctx, u.ID)
	if err != nil || !ok || claimed.State != ProviderOutboxSending || claimed.AttemptCount != 1 || claimed.ThreadID != "gmail-thread-1" {
		t.Fatalf("claim=%+v ok=%v err=%v", claimed, ok, err)
	}
	n, err := st.RecoverInterruptedProviderOutbox(ctx, u.ID)
	if err != nil || n != 1 {
		t.Fatalf("recover n=%d err=%v", n, err)
	}
	got, err := st.GetProviderOutboxJob(ctx, u.ID, first.ID)
	if err != nil || got.State != ProviderOutboxUnknown || got.ThreadID != "gmail-thread-1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	if _, ok, err := st.ClaimNextProviderOutbox(ctx, u.ID); err != nil || ok {
		t.Fatalf("unknown job was automatically retried ok=%v err=%v", ok, err)
	}
}
