package mailhub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/blob"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestMockEvidenceSurvivesMirrorDeletionAndDeduplicatesReplay(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.OpenServer(filepath.Join(root, "system.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	index, err := search.OpenPerUser(filepath.Join(root, "indexes"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	user, err := st.CreateUser(ctx, "gate-d@example.invalid", "Gate D", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	hub := Service{Store: st, Blobs: blob.New(root), Search: index}
	for _, account := range provider.MockAccounts(user.ID) {
		if err := hub.Sync(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	observations, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != 6 {
		t.Fatalf("initial evidence observations=%d, want 6", len(observations))
	}
	complete, oversized := 0, 0
	rawPaths := map[string]bool{}
	var personalPlanning, workPlanning store.EvidenceObservation
	for _, obs := range observations {
		switch obs.CaptureStatus {
		case store.EvidenceCaptureComplete:
			complete++
			if obs.RawSHA256 == "" || obs.RawPath == "" || obs.RawSize <= 0 {
				t.Fatalf("complete evidence missing raw metadata: %+v", obs)
			}
			rawPaths[obs.RawPath] = true
		case store.EvidenceCaptureOversized:
			oversized++
			if obs.RawSHA256 != "" || obs.RawPath != "" || obs.RawSize != 0 {
				t.Fatalf("oversized stub claimed complete evidence: %+v", obs)
			}
		default:
			t.Fatalf("unexpected capture status: %+v", obs)
		}
		if obs.ProviderMessageID == "planning" && obs.SourceAccount == "personal" {
			personalPlanning = obs
		}
		if obs.ProviderMessageID == "planning" && obs.SourceAccount == "work" {
			workPlanning = obs
		}
	}
	if complete != 5 || oversized != 1 || len(rawPaths) != 3 {
		t.Fatalf("complete=%d oversized=%d unique raw=%d, want 5/1/3", complete, oversized, len(rawPaths))
	}
	if personalPlanning.ID == 0 || workPlanning.ID == 0 || personalPlanning.ID == workPlanning.ID || personalPlanning.RawPath != workPlanning.RawPath {
		t.Fatalf("cross-account provenance/raw dedup mismatch: personal=%+v work=%+v", personalPlanning, workPlanning)
	}
	events, err := st.PendingEvidenceEvents(ctx, user.ID, 50, time.Now().UTC().Add(time.Second))
	if err != nil || len(events) != 6 {
		t.Fatalf("initial evidence outbox=%d err=%v", len(events), err)
	}

	personal := provider.MockAccounts(user.ID)[0]
	if err := hub.Mutate(ctx, personal, provider.Mutation{MessageID: "planning", Action: "label", Value: true, Label: "verified"}); err != nil {
		t.Fatal(err)
	}
	observations, err = st.EvidenceObservations(ctx, user.ID)
	if err != nil || len(observations) != 7 {
		t.Fatalf("label observation history=%d err=%v", len(observations), err)
	}
	if err := st.SetProviderCursor(ctx, user.ID, "personal", ""); err != nil {
		t.Fatal(err)
	}
	if err := hub.Sync(ctx, personal); err != nil {
		t.Fatal(err)
	}
	observations, err = st.EvidenceObservations(ctx, user.ID)
	if err != nil || len(observations) != 7 {
		t.Fatalf("replay duplicated evidence observations=%d err=%v", len(observations), err)
	}
	events, err = st.PendingEvidenceEvents(ctx, user.ID, 50, time.Now().UTC().Add(time.Second))
	if err != nil || len(events) != 7 {
		t.Fatalf("replay duplicated evidence outbox=%d err=%v", len(events), err)
	}

	var deleteObservation store.EvidenceObservation
	for _, obs := range observations {
		if obs.SourceAccount == "personal" && obs.ProviderMessageID == "reply" && obs.CaptureStatus == store.EvidenceCaptureComplete {
			deleteObservation = obs
			break
		}
	}
	if deleteObservation.ID == 0 || deleteObservation.MirrorMessageID == 0 {
		t.Fatalf("missing mirror-linked evidence observation: %+v", deleteObservation)
	}
	if err := st.DeleteMessageForUser(ctx, user.ID, deleteObservation.MirrorMessageID); err != nil {
		t.Fatal(err)
	}
	afterDelete, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, obs := range afterDelete {
		if obs.ID == deleteObservation.ID {
			found = true
			if obs.MirrorMessageID != 0 || obs.RawPath != deleteObservation.RawPath || obs.RawSHA256 != deleteObservation.RawSHA256 {
				t.Fatalf("mirror delete damaged evidence: before=%+v after=%+v", deleteObservation, obs)
			}
			if _, err := os.Stat(filepath.Join(root, obs.RawPath)); err != nil {
				t.Fatalf("mirror delete removed evidence raw: %v", err)
			}
		}
	}
	if !found {
		t.Fatal("mirror delete removed evidence observation")
	}
}

func TestMockEvidenceFailureIsRecordedAndRetryable(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	st, err := store.OpenServer(filepath.Join(root, "system.db"), root)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	index, err := search.OpenPerUser(filepath.Join(root, "indexes"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	user, err := st.CreateUser(ctx, "gate-d-retry@example.invalid", "Gate D Retry", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	account := provider.MockAccounts(user.ID)[0]
	badHub := Service{Store: st, Blobs: blob.New(blocked), Search: index}
	if err := badHub.Sync(ctx, account); err == nil {
		t.Fatal("evidence filesystem failure did not fail the sync")
	}
	source, err := st.EnsureMockSource(ctx, user.ID, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if source.Cursor != "" {
		t.Fatalf("failed evidence capture advanced provider cursor=%q", source.Cursor)
	}
	observations, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil || len(observations) != 1 || observations[0].CaptureStatus != store.EvidenceCaptureFailed {
		t.Fatalf("failed capture observation=%+v err=%v", observations, err)
	}
	goodHub := Service{Store: st, Blobs: blob.New(root), Search: index}
	if err := goodHub.Sync(ctx, account); err != nil {
		t.Fatal(err)
	}
	observations, err = st.EvidenceObservations(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed, complete := 0, 0
	for _, obs := range observations {
		if obs.CaptureStatus == store.EvidenceCaptureFailed {
			failed++
		}
		if obs.CaptureStatus == store.EvidenceCaptureComplete {
			complete++
		}
	}
	if failed != 1 || complete != 3 {
		t.Fatalf("retry evidence statuses failed=%d complete=%d observations=%+v", failed, complete, observations)
	}
	source, err = st.EnsureMockSource(ctx, user.ID, account.ID)
	if err != nil || source.Cursor == "" {
		t.Fatalf("successful retry did not advance cursor=%q err=%v", source.Cursor, err)
	}
}
