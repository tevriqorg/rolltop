package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"rolltop/backend/provider"
)

// TestSyntheticGmailAcceptance is opt-in so CI stays hermetic. Vela runs it
// against an isolated tools/mock-gmail-source process with a temporary state
// directory; it never needs Google credentials or real mail.
func TestSyntheticGmailAcceptance(t *testing.T) {
	root := strings.TrimRight(os.Getenv("ROLLTOP_SYNTHETIC_GMAIL_ROOT"), "/")
	if root == "" {
		t.Skip("synthetic Gmail acceptance endpoint not configured")
	}
	control := root + "/accounts/personal"
	base := control + "/gmail/v1"
	httpClient := &http.Client{Timeout: 5 * time.Second}
	client := Client{BaseURL: base, AccessToken: "synthetic", HTTPClient: httpClient, InitialBackoff: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }}
	ctx := context.Background()
	a := provider.Account{UserID: 1, ID: "google-synthetic", Provider: provider.Gmail}
	census, err := client.Census(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if census.MessageCount != 3 || census.EstimatedRawBytes <= 0 {
		t.Fatalf("census=%+v", census)
	}
	// Metadata mode must not leak raw body data from the test source.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/users/me/messages/p1?format=metadata", nil)
	req.Header.Set("Authorization", "Bearer synthetic")
	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if bytes.Contains(body, []byte(`"raw"`)) {
		t.Fatalf("metadata response leaked raw: %s", body)
	}
	p := Provider{Client: client, Account: a}
	var got []provider.Message
	state, err := p.Fetch(ctx, a, provider.State{Account: a}, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || state.Cursor == "" {
		t.Fatalf("full got=%d state=%+v", len(got), state)
	}
	if err := client.Mutate(ctx, provider.Mutation{MessageID: "p1", Action: "read", Value: true}); err != nil {
		t.Fatal(err)
	}
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Read {
		t.Fatalf("history mutation=%+v", got)
	}
	if err := client.Mutate(ctx, provider.Mutation{MessageID: "p1", Action: "trash", Value: true}); err != nil {
		t.Fatal(err)
	}
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || len(got[0].Folders) == 0 || got[0].Folders[0] != "Trash" {
		t.Fatalf("trash mutation=%+v", got)
	}
	if err := client.Mutate(ctx, provider.Mutation{MessageID: "p1", Action: "restore", Value: true}); err != nil {
		t.Fatal(err)
	}
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Mutate(ctx, provider.Mutation{MessageID: "p1", Action: "label", Value: true, Label: "CATEGORY_UPDATES"}); err != nil {
		t.Fatal(err)
	}
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !containsString(got[0].Labels, "CATEGORY_UPDATES") {
		t.Fatalf("label mutation=%+v", got)
	}
	raw := []byte("From: vipdonmen.personal@example.test\r\nTo: dest@example.test\r\nSubject: Synthetic native send\r\nMessage-ID: <synthetic-send@example.test>\r\n\r\nhello\r\n")
	sent, err := client.SendRaw(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	if sent.ID == "" {
		t.Fatal("send missing native id")
	}
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range got {
		if m.ID == sent.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("sent message missing from history: %+v", got)
	}
	postJSON := func(path string, in any) {
		b, _ := json.Marshal(in)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, control+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode/100 != 2 {
			payload, _ := io.ReadAll(res.Body)
			t.Fatalf("control %s: %d %s", path, res.StatusCode, payload)
		}
	}
	postJSON("/__control__/expire-history", map[string]any{"minHistoryId": "999999"})
	_, err = p.Fetch(ctx, a, state, func(provider.Message) error { return nil })
	if !errors.Is(err, ErrHistoryExpired) {
		t.Fatalf("history expiry err=%v", err)
	}
	postJSON("/__control__/clear-history-expiry", map[string]any{})
	// One injected 503 must be retried inside the client and still complete a full fetch.
	postJSON("/__control__/fail-next", map[string]any{"contains": "/profile", "status": 503, "count": 1})
	got = nil
	_, err = p.Fetch(ctx, a, provider.State{Account: a}, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 4 {
		t.Fatalf("retry full got=%d", len(got))
	}
	postJSON("/__control__/delete", map[string]any{"id": sent.ID})
	got = nil
	state, err = p.Fetch(ctx, a, state, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	deleted := false
	for _, m := range got {
		if m.ID == sent.ID && m.Deleted {
			deleted = true
		}
	}
	if !deleted {
		t.Fatalf("source delete missing from history: %+v", got)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
