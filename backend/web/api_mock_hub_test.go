package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/mailhub"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestMockHubHTTPIngestionAuthMutationsAndAttachmentIsolation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	index, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	owner, err := st.CreateUser(ctx, "http-owner@example.invalid", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "http-other@example.invalid", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, blobs: blob.New(dir), search: index, masterKey: []byte("12345678901234567890123456789012")}
	request := func(user store.User, method, path, payload string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(payload))
		if user.ID > 0 {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: user}))
		}
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "mock-csrf"})
			r.Header.Set("X-CSRF-Token", s.csrfForBase("mock-csrf"))
		}
		w := httptest.NewRecorder()
		if strings.HasPrefix(path, "/attachments/") {
			s.handleAttachment(w, r)
		} else {
			s.handleAPI(w, r)
		}
		return w
	}
	if w := request(store.User{}, "GET", "/api/mock-hub", "", false); w.Code != 401 {
		t.Fatalf("anonymous: %d", w.Code)
	}
	if w := request(owner, "POST", "/api/mock-hub/sync", "{}", false); w.Code != 403 {
		t.Fatalf("missing CSRF: %d", w.Code)
	}
	if w := request(owner, "GET", "/api/mock-hub", "", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"message_count":4`) {
		t.Fatalf("pre-sync census: %d %s", w.Code, w.Body.String())
	}
	if w := request(owner, "POST", "/api/mock-hub/sync", "{}", true); w.Code != 200 {
		t.Fatalf("sync: %d %s", w.Code, w.Body.String())
	}
	if w := request(other, "GET", "/api/mock-hub?user_id=1", "", false); w.Code != 200 || !strings.Contains(w.Body.String(), `"messages":[]`) {
		t.Fatalf("tenant query: %d %s", w.Code, w.Body.String())
	}
	change := `{"account":"personal","mutation":{"message_id":"planning","action":"label","value":true,"label":"verified"}}`
	if w := request(owner, "POST", "/api/mock-hub/mutate", change, true); w.Code != 200 {
		t.Fatalf("mutation: %d %s", w.Code, w.Body.String())
	}
	if w := request(owner, "POST", "/api/mock-hub/mutate", `{"user_id":2,"account":"personal","mutation":{"message_id":"planning","action":"trash"}}`, true); w.Code != 400 {
		t.Fatal("browser user_id accepted")
	}
	if w := request(owner, "POST", "/api/mock-hub/mutate", `{"account":"personal","mutation":{"message_id":"planning","action":"send"}}`, true); w.Code != 400 {
		t.Fatal("send accepted")
	}
	hub := mailhub.Service{Store: st, Blobs: s.blobs, Search: index}
	messages, err := hub.Messages(ctx, owner.ID, mailhub.Filter{Query: "notes.txt"})
	if err != nil || len(messages) != 2 {
		t.Fatalf("attachment search: %v", err)
	}
	id := messages[0].Attachments[0].ID
	path := fmt.Sprintf("/attachments/%d/download", id)
	w := request(owner, "GET", path, "", false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "固定附件") {
		t.Fatalf("attachment: %d %s", w.Code, w.Body.String())
	}
	if w := request(other, "GET", path, "", false); w.Code != 404 {
		t.Fatal("attachment crossed tenants")
	}
	w = request(owner, "GET", "/api/mock-hub?label=verified", "", false)
	var data struct {
		Messages    []mailhub.Message `json:"messages"`
		Credentials int               `json:"real_credentials"`
		OAuth       int               `json:"oauth_accounts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Messages) != 1 || data.Credentials != 0 || data.OAuth != 0 {
		t.Fatal("mutation/zero-credential acceptance failed")
	}
	if !isAppRoute("/mock") {
		t.Fatal("Mock route unavailable on reload")
	}
}
