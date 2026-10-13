package evidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/provider"
	"rolltop/backend/store"
)

func TestCaptureDeduplicatesRawWhilePreservingProvenance(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, "evidence@example.invalid", "Evidence", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: sender@example.invalid\r\nTo: recipient@example.invalid\r\nMessage-ID: <same@example.invalid>\r\nSubject: Evidence\r\n\r\nbody\r\n")
	svc := Service{Store: st, Root: root}
	input := Input{UserID: user.ID, Provider: provider.Mock, SourceAccount: "personal", ProviderMessageID: "same", ThreadID: "thread", Folders: []string{"INBOX"}, Labels: []string{"important"}, MessageIDHeader: "<same@example.invalid>", SourceSize: int64(len(raw)), Raw: raw}
	first, err := svc.Capture(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Capture(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID || first.ObservationKey != again.ObservationKey {
		t.Fatalf("exact replay created a new observation: first=%+v again=%+v", first, again)
	}
	if first.CaptureStatus != store.EvidenceCaptureComplete || first.RawSHA256 == "" || first.RawSize != int64(len(raw)) {
		t.Fatalf("complete observation=%+v", first)
	}
	if !strings.HasPrefix(filepath.ToSlash(first.RawPath), "evidence/mail/sha256/") || !strings.HasSuffix(first.RawPath, ".eml") {
		t.Fatalf("raw path=%q", first.RawPath)
	}
	got, err := os.ReadFile(filepath.Join(root, first.RawPath))
	if err != nil || string(got) != string(raw) {
		t.Fatalf("raw read=%q err=%v", got, err)
	}
	info, err := os.Stat(filepath.Join(root, first.RawPath))
	if err != nil || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("evidence permissions=%v err=%v", info.Mode().Perm(), err)
	}

	otherSource := input
	otherSource.SourceAccount = "work"
	second, err := svc.Capture(ctx, otherSource)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || second.RawPath != first.RawPath || second.RawSHA256 != first.RawSHA256 {
		t.Fatalf("same raw should share object but preserve source observation: first=%+v second=%+v", first, second)
	}
	labelChange := input
	labelChange.Labels = []string{"important", "verified"}
	third, err := svc.Capture(ctx, labelChange)
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID || third.RawPath != first.RawPath {
		t.Fatalf("label observation not preserved independently: first=%+v third=%+v", first, third)
	}
	observations, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil || len(observations) != 3 {
		t.Fatalf("observations=%d err=%v", len(observations), err)
	}
	events, err := st.PendingEvidenceEvents(ctx, user.ID, 20, time.Now().UTC().Add(time.Second))
	if err != nil || len(events) != 3 {
		t.Fatalf("outbox events=%d err=%v", len(events), err)
	}
}

func TestOversizedAndFailedCaptureNeverClaimCompleteRaw(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, "status@example.invalid", "Status", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	svc := Service{Store: st, Root: root}
	oversized, err := svc.Capture(ctx, Input{UserID: user.ID, Provider: provider.Mock, SourceAccount: "personal", ProviderMessageID: "large", ThreadID: "large", Folders: []string{"INBOX"}, MessageIDHeader: "<large@example.invalid>", SourceSize: 51 * 1024 * 1024, Raw: []byte("header stub"), Oversized: true})
	if err != nil {
		t.Fatal(err)
	}
	if oversized.CaptureStatus != store.EvidenceCaptureOversized || oversized.RawSHA256 != "" || oversized.RawPath != "" || oversized.RawSize != 0 {
		t.Fatalf("oversized observation claimed raw: %+v", oversized)
	}

	blocked := filepath.Join(root, "blocked-root")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	failedSvc := Service{Store: st, Root: blocked}
	failed, err := failedSvc.Capture(ctx, Input{UserID: user.ID, Provider: provider.Mock, SourceAccount: "personal", ProviderMessageID: "failed", ThreadID: "failed", Folders: []string{"INBOX"}, MessageIDHeader: "<failed@example.invalid>", SourceSize: 100, Raw: []byte("From: x@example.invalid\r\n\r\nbody")})
	if err == nil {
		t.Fatal("filesystem capture failure unexpectedly succeeded")
	}
	if failed.CaptureStatus != store.EvidenceCaptureFailed || failed.RawSHA256 != "" || failed.RawPath != "" || failed.RawSize != 0 || failed.LastError == "" {
		t.Fatalf("failed observation claimed raw: %+v", failed)
	}
}

func TestCaptureRetryAfterCrashWindowReusesDurableRaw(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "rolltop.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	user, err := st.CreateUser(ctx, "retry@example.invalid", "Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: retry@example.invalid\r\nMessage-ID: <retry@example.invalid>\r\n\r\nretry")
	input := Input{UserID: user.ID, Provider: provider.Mock, SourceAccount: "personal", ProviderMessageID: "retry", ThreadID: "retry", Folders: []string{"INBOX"}, MessageIDHeader: "<retry@example.invalid>", SourceSize: int64(len(raw)), Raw: raw}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := (Service{Store: st, Root: root}).Capture(ctx, input); err == nil {
		t.Fatal("capture with closed metadata store unexpectedly succeeded")
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	rawPath := filepath.Join(root, "evidence", "mail", "sha256", hash[:2], hash[2:4], hash+".eml")
	if _, err := os.Stat(rawPath); err != nil {
		t.Fatalf("durable raw missing after crash window: %v", err)
	}

	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	obs, err := (Service{Store: st, Root: root}).Capture(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if obs.CaptureStatus != store.EvidenceCaptureComplete || obs.RawSHA256 != hash {
		t.Fatalf("retry observation=%+v", obs)
	}
	observations, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil || len(observations) != 1 {
		t.Fatalf("retry observations=%d err=%v", len(observations), err)
	}
	events, err := st.PendingEvidenceEvents(ctx, user.ID, 10, time.Now().UTC().Add(time.Second))
	if err != nil || len(events) != 1 {
		t.Fatalf("retry outbox=%d err=%v", len(events), err)
	}

	next := time.Now().UTC().Add(time.Hour)
	if err := st.RetryEvidenceEvent(ctx, user.ID, events[0].ID, "consumer unavailable", next); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if pending, err := st.PendingEvidenceEvents(ctx, user.ID, 10, time.Now().UTC()); err != nil || len(pending) != 0 {
		t.Fatalf("event retry schedule pending=%d err=%v", len(pending), err)
	}
	if pending, err := st.PendingEvidenceEvents(ctx, user.ID, 10, next.Add(time.Second)); err != nil || len(pending) != 1 || pending[0].Attempts != 1 || pending[0].LastError != "consumer unavailable" {
		t.Fatalf("event retry not durable across restart: %+v err=%v", pending, err)
	}
	if err := st.DeliverEvidenceEvent(ctx, user.ID, events[0].ID, next.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if pending, err := st.PendingEvidenceEvents(ctx, user.ID, 10, next.Add(3*time.Second)); err != nil || len(pending) != 0 {
		t.Fatalf("delivered event still pending=%d err=%v", len(pending), err)
	}
}
