package web

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/accountconnector"
	"rolltop/backend/blob"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestGmailProviderComposeOutboxIsIdempotentAndSendsNative(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.CreateUser(ctx, "owner@gmail.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer idx.Close()
	var sentRaw []byte
	var sentThread string
	sourceRaw := []byte("From: Sender <sender@example.test>\r\nTo: owner@gmail.test\r\nSubject: Original Gmail\r\nMessage-ID: <original@gmail.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\noriginal body\r\n")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/users/me/messages/send":
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			sentRaw, _ = base64.RawURLEncoding.DecodeString(in["raw"])
			sentThread = in["threadId"]
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "gmail-sent-1", "threadId": "sent-t"})
		case r.URL.Path == "/users/me/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "owner@gmail.test", "historyId": "5"})
		case r.URL.Path == "/users/me/labels":
			_ = json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}}})
		case r.URL.Path == "/users/me/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "m1", "threadId": "orig-thread"}}})
		case r.URL.Path == "/users/me/messages/m1":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "m1", "threadId": "orig-thread", "historyId": "5", "labelIds": []string{"INBOX"}, "sizeEstimate": len(sourceRaw), "raw": base64.RawURLEncoding.EncodeToString(sourceRaw), "payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<original@gmail.test>"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	key := []byte("12345678901234567890123456789012")
	s := &Server{store: st, blobs: blob.New(dir), search: idx, masterKey: key, googleConnector: fakeGoogleConnector{}, accountConnectors: accountconnector.Service{Store: st, MasterKey: key}, gmailAPIBaseURL: api.URL, gmailHTTPClient: api.Client(), providerOutboxWake: make(chan struct{}, 1)}
	connection, _, err := s.accountConnectors.FinishAuthorization(ctx, u.ID, s.googleConnector, gmailCapabilities, "ok", accountconnector.PKCE{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.SaveProviderCensus(ctx, u.ID, string(provider.Gmail), googleSourceID(connection), 1, 100, 100, 150, "metadata only", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.ApproveProviderBackfill(ctx, u.ID, string(provider.Gmail), googleSourceID(connection)); err != nil {
		t.Fatal(err)
	}
	cu := currentUser{User: u}
	form := composeForm{To: "dest@example.test", Subject: "Provider native", Body: "hello via Gmail", SubmissionKey: "gmail-stable-send"}
	job, handled, err := s.queueProviderCompose(ctx, cu, form)
	if err != nil || !handled || job.State != store.ProviderOutboxPending {
		t.Fatalf("job=%+v handled=%v err=%v", job, handled, err)
	}
	again, handled, err := s.queueProviderCompose(ctx, cu, form)
	if err != nil || !handled || again.ID != job.ID {
		t.Fatalf("again=%+v handled=%v err=%v", again, handled, err)
	}
	if !s.processOneProviderOutboxJob(ctx) {
		t.Fatal("provider worker found no job")
	}
	got, err := st.GetProviderOutboxJob(ctx, u.ID, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != store.ProviderOutboxSent || got.NativeMessageID != "gmail-sent-1" || got.AttemptCount != 1 {
		t.Fatalf("sent job=%+v", got)
	}
	text := string(sentRaw)
	if !strings.Contains(text, "To: <dest@example.test>") || !strings.Contains(text, "Subject: Provider native") || !strings.Contains(text, "hello via Gmail") {
		t.Fatalf("sent raw=%q", text)
	}
	if s.processOneProviderOutboxJob(ctx) {
		t.Fatal("sent job was automatically re-sent")
	}

	// A reply to a Gmail-managed message must stay on the native Gmail thread.
	if err := s.syncApprovedGmail(ctx, u.ID, connection); err != nil {
		t.Fatal(err)
	}
	providerMessages, err := st.ProviderMessagesFor(ctx, u.ID, string(provider.Gmail))
	if err != nil || len(providerMessages) != 1 {
		t.Fatalf("provider messages=%+v err=%v", providerMessages, err)
	}
	reply := composeForm{To: "sender@example.test", Subject: "Re: Original Gmail", Body: "reply via Gmail", InReplyToID: providerMessages[0].ID, SubmissionKey: "gmail-reply-thread"}
	replyJob, handled, err := s.queueProviderCompose(ctx, cu, reply)
	if err != nil || !handled || replyJob.ThreadID != "orig-thread" {
		t.Fatalf("reply job=%+v handled=%v err=%v", replyJob, handled, err)
	}
	if !s.processOneProviderOutboxJob(ctx) {
		t.Fatal("provider worker found no reply job")
	}
	if sentThread != "orig-thread" {
		t.Fatalf("gmail reply thread=%q", sentThread)
	}
	replyRaw := string(sentRaw)
	if !strings.Contains(replyRaw, "In-Reply-To: <original@gmail.test>") || !strings.Contains(replyRaw, "reply via Gmail") {
		t.Fatalf("reply raw=%q", replyRaw)
	}
}
