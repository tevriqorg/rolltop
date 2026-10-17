package mailhub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/provider"
	gmailprovider "rolltop/backend/provider/gmail"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

type gmailHubFixture struct {
	mu            sync.Mutex
	history       string
	messages      map[string]map[string]any
	events        []map[string]any
	expireHistory bool
}

func gmailAttachmentRaw(id, subject string) []byte {
	return []byte("From: Sender <sender@example.test>\r\nTo: owner@gmail.test\r\nSubject: " + subject + "\r\nMessage-ID: <" + id + "@gmail.test>\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"b\"\r\n\r\n--b\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody " + subject + "\r\n--b\r\nContent-Type: text/plain; name=\"notes.txt\"\r\nContent-Disposition: attachment; filename=\"notes.txt\"\r\nContent-Transfer-Encoding: base64\r\n\r\nbm90ZXMtZnJvbS1nbWFpbA==\r\n--b--\r\n")
}

func newGmailHubFixture() *gmailHubFixture {
	raw := gmailAttachmentRaw("m1", "Initial Gmail")
	return &gmailHubFixture{history: "100", messages: map[string]map[string]any{
		"m1": {"id": "m1", "threadId": "thread-1", "historyId": "100", "labelIds": []string{"INBOX", "UNREAD", "L1", "L2"}, "sizeEstimate": len(raw), "raw": base64.RawURLEncoding.EncodeToString(raw), "payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<m1@gmail.test>"}}}},
	}}
}

func (f *gmailHubFixture) server(t *testing.T) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/users/me/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "owner@gmail.test", "historyId": f.history})
		case r.URL.Path == "/users/me/labels":
			_ = json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}, {"id": "UNREAD", "name": "UNREAD"}, {"id": "L1", "name": "Project"}, {"id": "L2", "name": "Family"}}})
		case r.URL.Path == "/users/me/messages":
			refs := []map[string]string{}
			for _, id := range []string{"m1", "m2"} {
				if m := f.messages[id]; m != nil {
					refs = append(refs, map[string]string{"id": id, "threadId": m["threadId"].(string)})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": refs})
		case r.URL.Path == "/users/me/history":
			if f.expireHistory {
				http.Error(w, "expired", http.StatusNotFound)
				return
			}
			start := r.URL.Query().Get("startHistoryId")
			_ = start
			_ = json.NewEncoder(w).Encode(map[string]any{"history": f.events, "historyId": f.history})
		case strings.HasPrefix(r.URL.Path, "/users/me/messages/"):
			id, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/users/me/messages/"))
			m := f.messages[id]
			if m == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(m)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestGmailMirrorRestartDedupAttachmentHistoryRecoveryAndEvidenceRetention(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dbPath := filepath.Join(root, "mail.db")
	indexPath := filepath.Join(root, "indexes")
	f := newGmailHubFixture()
	api := f.server(t)
	defer api.Close()
	open := func() (*store.Store, *search.Service, Service) {
		st, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := search.OpenPerUser(indexPath)
		if err != nil {
			st.Close()
			t.Fatal(err)
		}
		return st, idx, Service{Store: st, Blobs: blob.New(root), Search: idx}
	}
	st, idx, hub := open()
	user, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	a := provider.Account{UserID: user.ID, ID: "google-1", Provider: provider.Gmail}
	client := gmailprovider.Client{BaseURL: api.URL, AccessToken: "token", HTTPClient: api.Client(), MaxRawMessageBytes: 4 << 20}
	if err := hub.SyncGmail(ctx, a, "Owner", "owner@gmail.test", client); err != nil {
		t.Fatal(err)
	}
	meta, err := st.ProviderMessagesFor(ctx, user.ID, "gmail")
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 {
		t.Fatalf("initial provider messages=%+v", meta)
	}
	if meta[0].ThreadID != "thread-1" {
		t.Fatalf("thread=%q", meta[0].ThreadID)
	}
	attachments, err := st.ListAttachmentsForMessage(ctx, user.ID, meta[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Filename != "notes.txt" {
		t.Fatalf("attachments=%+v", attachments)
	}
	obs, err := st.EvidenceObservations(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(obs) != 1 || obs[0].Provider != "gmail" || obs[0].ProviderMessageID != "m1" {
		t.Fatalf("evidence=%+v", obs)
	}
	if err := hub.SyncGmail(ctx, a, "Owner", "owner@gmail.test", client); err != nil {
		t.Fatal(err)
	}
	meta, _ = st.ProviderMessagesFor(ctx, user.ID, "gmail")
	if len(meta) != 1 {
		t.Fatalf("duplicate after repeat=%+v", meta)
	}
	if err := idx.Close(); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, idx, hub = open()
	defer st.Close()
	defer idx.Close()
	f.mu.Lock()
	raw2 := gmailAttachmentRaw("m2", "Incremental Gmail")
	f.messages["m2"] = map[string]any{"id": "m2", "threadId": "thread-2", "historyId": "101", "labelIds": []string{"INBOX"}, "sizeEstimate": len(raw2), "raw": base64.RawURLEncoding.EncodeToString(raw2), "payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<m2@gmail.test>"}}}}
	f.history = "101"
	f.events = []map[string]any{{"id": "101", "messagesAdded": []map[string]any{{"message": map[string]any{"id": "m2", "threadId": "thread-2"}}}}}
	f.mu.Unlock()
	if err := hub.SyncGmail(ctx, a, "Owner", "owner@gmail.test", client); err != nil {
		t.Fatal(err)
	}
	meta, _ = st.ProviderMessagesFor(ctx, user.ID, "gmail")
	if len(meta) != 2 {
		t.Fatalf("incremental messages=%+v", meta)
	}
	// Force native history expiry. The persisted cursor is cleared before full reconciliation.
	f.mu.Lock()
	f.expireHistory = true
	f.events = nil
	f.mu.Unlock()
	if err := hub.SyncGmail(ctx, a, "Owner", "owner@gmail.test", client); err != nil {
		t.Fatal(err)
	}
	meta, _ = st.ProviderMessagesFor(ctx, user.ID, "gmail")
	if len(meta) != 2 {
		t.Fatalf("recovery duplicated/lost messages=%+v", meta)
	}
	// Resume history and delete m1 at the source; mutable Mirror disappears while Evidence remains.
	f.mu.Lock()
	f.expireHistory = false
	delete(f.messages, "m1")
	f.history = "102"
	f.events = []map[string]any{{"id": "102", "messagesDeleted": []map[string]any{{"message": map[string]any{"id": "m1", "threadId": "thread-1"}}}}}
	f.mu.Unlock()
	if err := hub.SyncGmail(ctx, a, "Owner", "owner@gmail.test", client); err != nil {
		t.Fatal(err)
	}
	meta, _ = st.ProviderMessagesFor(ctx, user.ID, "gmail")
	if len(meta) != 1 || meta[0].NativeID != "m2" {
		t.Fatalf("delete mirror=%+v", meta)
	}
	obs, err = st.EvidenceObservations(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range obs {
		if o.ProviderMessageID == "m1" {
			found = true
		}
	}
	if !found {
		t.Fatalf("source delete removed evidence: %+v", obs)
	}
}
