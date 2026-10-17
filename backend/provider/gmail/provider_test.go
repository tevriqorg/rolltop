package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"rolltop/backend/provider"
)

type gmailFixture struct {
	mu            sync.Mutex
	history       string
	messages      map[string]map[string]any
	events        []map[string]any
	modifyBodies  []map[string]any
	sentRaw       []byte
	sentThread    string
	failProfile   int
	expireHistory bool
}

func newGmailFixture() *gmailFixture {
	raw1 := []byte("From: Sender <sender@example.test>\r\nTo: owner@example.test\r\nSubject: One\r\nMessage-ID: <one@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nhello one\r\n")
	raw2 := []byte("From: Sender <sender@example.test>\r\nTo: owner@example.test\r\nSubject: Two\r\nMessage-ID: <two@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nhello two\r\n")
	return &gmailFixture{history: "12", messages: map[string]map[string]any{
		"m1": {"id": "m1", "threadId": "t1", "historyId": "11", "labelIds": []string{"INBOX", "UNREAD", "STARRED", "L1"}, "sizeEstimate": len(raw1), "raw": base64.RawURLEncoding.EncodeToString(raw1), "payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<one@example.test>"}, {"name": "Subject", "value": "One"}}}},
		"m2": {"id": "m2", "threadId": "t1", "historyId": "12", "labelIds": []string{"INBOX"}, "sizeEstimate": len(raw2), "raw": base64.RawURLEncoding.EncodeToString(raw2), "payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<two@example.test>"}}}},
	}}
}
func (f *gmailFixture) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/users/me/profile":
			if f.failProfile > 0 {
				f.failProfile--
				w.Header().Set("Retry-After", "0")
				http.Error(w, "retry", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "owner@example.test", "historyId": f.history})
		case r.URL.Path == "/users/me/labels":
			_ = json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "L1", "name": "Project"}, {"id": "INBOX", "name": "INBOX"}, {"id": "STARRED", "name": "STARRED"}, {"id": "UNREAD", "name": "UNREAD"}, {"id": "TRASH", "name": "TRASH"}}})
		case r.URL.Path == "/users/me/messages" && r.Method == http.MethodGet:
			refs := []map[string]string{}
			for _, id := range []string{"m1", "m2"} {
				if m := f.messages[id]; m != nil {
					refs = append(refs, map[string]string{"id": id, "threadId": m["threadId"].(string)})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": refs})
		case r.URL.Path == "/users/me/history":
			if got := r.URL.Query()["historyTypes"]; len(got) != 4 {
				t.Errorf("historyTypes=%v", got)
			}
			if f.expireHistory {
				http.Error(w, "expired", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"history": f.events, "historyId": f.history})
		case strings.HasPrefix(r.URL.Path, "/users/me/messages/") && strings.HasSuffix(r.URL.Path, "/modify"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/users/me/messages/"), "/modify")
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.modifyBodies = append(f.modifyBodies, body)
			m := f.messages[id]
			if m == nil {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(m)
		case r.URL.Path == "/users/me/messages/send":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			f.sentRaw, _ = base64.RawURLEncoding.DecodeString(body["raw"])
			f.sentThread = body["threadId"]
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sent-1", "threadId": "sent-thread", "labelIds": []string{"SENT"}})
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

func TestProviderFullHistoryMutationSendRetryAndExpiry(t *testing.T) {
	f := newGmailFixture()
	f.failProfile = 1
	srv := f.server(t)
	defer srv.Close()
	client := Client{BaseURL: srv.URL, AccessToken: "token", HTTPClient: srv.Client(), InitialBackoff: time.Millisecond, Sleep: func(context.Context, time.Duration) error { return nil }}
	a := provider.Account{UserID: 7, ID: "google-1", Provider: provider.Gmail}
	p := Provider{Client: client, Account: a}
	var got []provider.Message
	next, err := p.Fetch(context.Background(), a, provider.State{Account: a}, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if next.Cursor != "12" || len(got) != 2 {
		t.Fatalf("next=%+v got=%d", next, len(got))
	}
	if got[0].ID != "m1" || got[0].Read || !got[0].Starred || got[0].MessageID != "<one@example.test>" || len(got[0].Labels) != 1 || got[0].Labels[0] != "Project" || got[0].ThreadID != "t1" || got[1].ThreadID != "t1" {
		t.Fatalf("m1=%+v", got[0])
	}
	f.mu.Lock()
	f.history = "15"
	f.events = []map[string]any{
		{"id": "13", "labelsRemoved": []map[string]any{{"message": map[string]any{"id": "m1", "threadId": "t1"}}}},
		{"id": "14", "messagesDeleted": []map[string]any{{"message": map[string]any{"id": "m2", "threadId": "t1"}}}},
	}
	m1 := f.messages["m1"]
	m1["labelIds"] = []string{"INBOX", "L1"}
	delete(f.messages, "m2")
	f.mu.Unlock()
	got = nil
	next2, err := p.Fetch(context.Background(), a, next, func(m provider.Message) error { got = append(got, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if next2.Cursor != "15" || len(got) != 2 || got[0].ID != "m1" || !got[0].Read || got[1].ID != "m2" || !got[1].Deleted {
		t.Fatalf("history next=%+v got=%+v", next2, got)
	}
	if err := client.Mutate(context.Background(), provider.Mutation{MessageID: "m1", Action: "label", Value: true, Label: "Project"}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	body := f.modifyBodies[len(f.modifyBodies)-1]
	f.mu.Unlock()
	if !strings.Contains(toJSON(body), "L1") {
		t.Fatalf("modify=%v", body)
	}
	raw := []byte("From: owner@example.test\r\nTo: x@example.test\r\nMessage-ID: <sent@example.test>\r\n\r\nhello")
	sent, err := client.SendRawInThread(context.Background(), raw, "thread-original")
	if err != nil || sent.ID != "sent-1" {
		t.Fatalf("sent=%+v err=%v", sent, err)
	}
	f.mu.Lock()
	same := string(f.sentRaw) == string(raw)
	f.mu.Unlock()
	if !same {
		t.Fatal("sent raw changed")
	}
	f.mu.Lock()
	thread := f.sentThread
	f.mu.Unlock()
	if thread != "thread-original" {
		t.Fatalf("sent thread=%q", thread)
	}
	f.mu.Lock()
	f.expireHistory = true
	f.mu.Unlock()
	_, err = p.Fetch(context.Background(), a, next2, func(provider.Message) error { return nil })
	if !errorsIs(err, ErrHistoryExpired) {
		t.Fatalf("expiry err=%v", err)
	}
}
func TestProviderOversizedUsesHeaderStub(t *testing.T) {
	f := newGmailFixture()
	f.messages["m1"]["sizeEstimate"] = int64(10 << 20)
	srv := f.server(t)
	defer srv.Close()
	a := provider.Account{UserID: 1, ID: "g", Provider: provider.Gmail}
	p := Provider{Client: Client{BaseURL: srv.URL, AccessToken: "token", HTTPClient: srv.Client(), MaxRawMessageBytes: 1024}, Account: a}
	var first provider.Message
	_, err := p.Fetch(context.Background(), a, provider.State{Account: a}, func(m provider.Message) error {
		if first.ID == "" {
			first = m
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Oversized || len(first.Raw) > 2048 || !strings.Contains(string(first.Raw), "X-Rolltop-Oversized: true") || first.MessageID != "<one@example.test>" {
		t.Fatalf("oversized=%+v raw=%q", first, string(first.Raw))
	}
}
func toJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
func errorsIs(err, target error) bool {
	return err != nil && strings.Contains(err.Error(), target.Error())
}
