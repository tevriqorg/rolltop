package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/blob"
	"rolltop/backend/mailai"
	"rolltop/backend/mailhub"
	"rolltop/backend/provider"
	"rolltop/backend/search"
	"rolltop/backend/store"
)

func TestMailAIProxyUsesServerResolvedMockContext(t *testing.T) {
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
	owner, err := st.CreateUser(ctx, "ai-owner@example.invalid", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "ai-other@example.invalid", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	hub := mailhub.Service{Store: st, Blobs: blob.New(dir), Search: index}
	for _, a := range provider.MockAccounts(owner.ID) {
		if err := hub.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range provider.MockAccounts(other.ID) {
		if err := hub.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := hub.Messages(ctx, owner.ID, mailhub.Filter{Query: "项目计划", Account: "personal"})
	if err != nil || len(messages) == 0 {
		t.Fatalf("messages=%d err=%v", len(messages), err)
	}
	messageID := messages[0].ID

	var received mailai.ChatRequest
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gate-e-secret" {
			t.Fatalf("sidecar auth=%q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"answer_delta\",\"text\":\"ok\"}\n\ndata: {\"type\":\"done\"}\n\n"))
	}))
	defer sidecar.Close()
	s := &Server{store: st, blobs: hub.Blobs, search: index, masterKey: []byte("12345678901234567890123456789012"), mailAIURL: sidecar.URL, mailAISharedToken: "gate-e-secret", mailAIHTTPClient: sidecar.Client()}

	r := httptest.NewRequest(http.MethodPost, "/api/mail-ai/chat", bytes.NewBufferString(`{"message_id":`+strconvInt64(messageID)+`,"prompt":"summarize"}`))
	r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: owner}))
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "ai-csrf"})
	r.Header.Set("X-CSRF-Token", s.csrfForBase("ai-csrf"))
	w := httptest.NewRecorder()
	s.handleAPI(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"answer_delta"`) {
		t.Fatalf("proxy=%d %s", w.Code, w.Body.String())
	}
	if received.UserID != owner.ID || received.Context.MessageID != messageID || received.Context.Provider != "mock" || received.Context.Account != "personal" || received.Context.Subject != "项目计划" {
		t.Fatalf("server context=%+v user=%d", received.Context, received.UserID)
	}
	if received.Context.Body == "" || received.Prompt != "summarize" {
		t.Fatalf("context/prompt=%+v", received)
	}

	// Unknown fields such as user_id/provider/account are rejected rather than trusted.
	forged := httptest.NewRequest(http.MethodPost, "/api/mail-ai/chat", bytes.NewBufferString(`{"message_id":`+strconvInt64(messageID)+`,"prompt":"x","user_id":`+strconvInt64(other.ID)+`}`))
	forged = forged.WithContext(context.WithValue(forged.Context(), userContextKey, currentUser{User: owner}))
	forged.AddCookie(&http.Cookie{Name: csrfCookie, Value: "ai-csrf"})
	forged.Header.Set("X-CSRF-Token", s.csrfForBase("ai-csrf"))
	fw := httptest.NewRecorder()
	s.handleAPI(fw, forged)
	if fw.Code != 400 {
		t.Fatalf("forged browser context accepted: %d %s", fw.Code, fw.Body.String())
	}
}

func TestMailAIToolsAreServerAuthenticatedTenantScopedAndConfirmMutations(t *testing.T) {
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
	owner, _ := st.CreateUser(ctx, "tools-owner@example.invalid", "Owner", "hash", false)
	other, _ := st.CreateUser(ctx, "tools-other@example.invalid", "Other", "hash", false)
	s := &Server{store: st, blobs: blob.New(dir), search: index, mailAISharedToken: "gate-e-secret"}
	hub := mailhub.Service{Store: st, Blobs: s.blobs, Search: index}
	for _, a := range provider.MockAccounts(owner.ID) {
		if err := hub.Sync(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	messages, _ := hub.Messages(ctx, owner.ID, mailhub.Filter{Account: "personal", Query: "项目计划"})
	messageID := messages[0].ID
	request := func(token string, in mailai.ToolRequest) *httptest.ResponseRecorder {
		body, _ := json.Marshal(in)
		r := httptest.NewRequest(http.MethodPost, "/api/mail-ai/tools", bytes.NewReader(body))
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		s.handleAPI(w, r)
		return w
	}
	if w := request("", mailai.ToolRequest{UserID: owner.ID, Name: "search_mail", Arguments: json.RawMessage(`{"q":"项目计划"}`)}); w.Code != 401 {
		t.Fatalf("anonymous tool=%d", w.Code)
	}
	if w := request("wrong", mailai.ToolRequest{UserID: owner.ID, Name: "search_mail", Arguments: json.RawMessage(`{"q":"项目计划"}`)}); w.Code != 401 {
		t.Fatalf("wrong token=%d", w.Code)
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: other.ID, Name: "get_message", Arguments: json.RawMessage(`{"message_id":` + strconvInt64(messageID) + `}`)}); w.Code != 404 {
		t.Fatalf("cross tenant=%d %s", w.Code, w.Body.String())
	}
	args := json.RawMessage(`{"message_id":` + strconvInt64(messageID) + `,"label":"ai-confirmed"}`)
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "add_label", Arguments: args}); w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed mutation=%d %s", w.Code, w.Body.String())
	}
	if got, _ := hub.Messages(ctx, owner.ID, mailhub.Filter{Label: "ai-confirmed"}); len(got) != 0 {
		t.Fatal("label mutated without confirmation")
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "add_label", Arguments: args, Confirmed: true}); w.Code != 200 {
		t.Fatalf("confirmed mutation=%d %s", w.Code, w.Body.String())
	}
	if got, _ := hub.Messages(ctx, owner.ID, mailhub.Filter{Label: "ai-confirmed"}); len(got) != 1 {
		t.Fatalf("confirmed label count=%d", len(got))
	}
}
