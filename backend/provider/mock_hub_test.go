package provider

import (
	"context"
	"reflect"
	"testing"
)

func TestMockMailboxJournalReplayCensusAndCheckpoints(t *testing.T) {
	ctx := context.Background()
	a := MockAccounts(7)[0]
	p := MockMailbox{Account: a}
	census, err := p.Census(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if census.MessageCount != 4 || census.EstimatedRawBytes < 51*1024*1024 || census.ExpectedEvidenceBytes != census.EstimatedRawBytes || census.RecommendedFreeDisk != (census.EstimatedRawBytes*3+1)/2 {
		t.Fatalf("census = %+v", census)
	}
	state := State{Account: a}
	calls := 0
	next, err := p.Fetch(ctx, a, state, func(Message) error { calls++; return nil })
	if err != nil || calls != 4 {
		t.Fatalf("initial: %d %v", calls, err)
	}
	calls = 0
	_, err = p.Fetch(ctx, a, next, func(Message) error { calls++; return nil })
	if err != nil || calls != 0 {
		t.Fatal("checkpoint replayed unchanged source")
	}
	changes := []Mutation{{"planning", "read", true, ""}, {"planning", "star", false, ""}, {"planning", "label", true, "review"}, {"planning", "label", false, "important"}, {"planning", "archive", false, ""}, {"attachment", "trash", false, ""}}
	for _, m := range changes {
		if err := p.Mutate(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := p.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	replay := MockMailbox{Account: a, Journal: append([]Mutation{}, p.Journal...)}
	again, err := replay.Snapshot(ctx)
	if err != nil || !reflect.DeepEqual(snapshot, again) {
		t.Fatal("journal replay is not deterministic")
	}
	if !snapshot[0].Read || snapshot[0].Starred || contains(snapshot[0].Folders, "INBOX") || !contains(snapshot[0].Labels, "review") || contains(snapshot[0].Labels, "important") || !contains(snapshot[2].Folders, "Trash") {
		t.Fatalf("mutations not reflected: %+v", snapshot)
	}
	failed := false
	checkpoint, err := p.Fetch(ctx, a, next, func(Message) error { failed = true; return context.Canceled })
	if !failed || err == nil || checkpoint != next {
		t.Fatal("failed projection advanced checkpoint")
	}
	for _, change := range []Mutation{{"missing", "read", true, ""}, {"planning", "send", true, ""}, {"planning", "label", true, ""}} {
		if err := p.Mutate(ctx, change); err == nil {
			t.Fatal("invalid mutation accepted")
		}
	}
	if _, err := p.Census(ctx, MockAccounts(8)[0]); err == nil {
		t.Fatal("cross-tenant census accepted")
	}
	if _, err := p.Fetch(ctx, a, State{Account: a, Cursor: "other-provider:state"}, func(Message) error { return nil }); err == nil {
		t.Fatal("invalid cursor accepted")
	}
}
