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
	"time"

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

	messageArg := json.RawMessage(`{"message_id":` + strconvInt64(messageID) + `}`)
	beforeStar, err := st.GetMessageForUser(ctx, owner.ID, messageID)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "unstar", Arguments: messageArg}); w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed unstar=%d %s", w.Code, w.Body.String())
	}
	if row, err := st.GetMessageForUser(ctx, owner.ID, messageID); err != nil || row.IsStarred != beforeStar.IsStarred {
		t.Fatalf("star state changed without confirmation before=%t row=%+v err=%v", beforeStar.IsStarred, row, err)
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "unstar", Arguments: messageArg, Confirmed: true}); w.Code != http.StatusOK {
		t.Fatalf("confirmed unstar=%d %s", w.Code, w.Body.String())
	}
	if row, err := st.GetMessageForUser(ctx, owner.ID, messageID); err != nil || row.IsStarred {
		t.Fatalf("confirmed unstar not persisted row=%+v err=%v", row, err)
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "star", Arguments: messageArg, Confirmed: true}); w.Code != http.StatusOK {
		t.Fatalf("confirmed star=%d %s", w.Code, w.Body.String())
	}
	if row, err := st.GetMessageForUser(ctx, owner.ID, messageID); err != nil || !row.IsStarred {
		t.Fatalf("confirmed star not persisted row=%+v err=%v", row, err)
	}

	until := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	snoozeArgs, _ := json.Marshal(map[string]any{"message_id": messageID, "until": until.Format(time.RFC3339)})
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "snooze", Arguments: snoozeArgs}); w.Code != http.StatusConflict {
		t.Fatalf("unconfirmed snooze=%d %s", w.Code, w.Body.String())
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "snooze", Arguments: snoozeArgs, Confirmed: true}); w.Code != http.StatusOK {
		t.Fatalf("confirmed snooze=%d %s", w.Code, w.Body.String())
	}
	if snooze, err := st.MessageSnoozeForUser(ctx, owner.ID, messageID); err != nil || !snooze.SnoozedUntil.Equal(until) {
		t.Fatalf("snooze=%+v err=%v want=%s", snooze, err, until)
	}

	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "trash", Arguments: messageArg, Confirmed: true}); w.Code != http.StatusOK {
		t.Fatalf("confirmed trash=%d %s", w.Code, w.Body.String())
	}
	if trashed, _ := hub.Messages(ctx, owner.ID, mailhub.Filter{View: "trash"}); len(trashed) == 0 {
		t.Fatal("trash action did not move the synthetic message into Trash")
	}
	if w := request("gate-e-secret", mailai.ToolRequest{UserID: owner.ID, Name: "restore", Arguments: messageArg, Confirmed: true}); w.Code != http.StatusOK {
		t.Fatalf("confirmed restore=%d %s", w.Code, w.Body.String())
	}
	if inbox, _ := hub.Messages(ctx, owner.ID, mailhub.Filter{View: "inbox"}); len(inbox) == 0 {
		t.Fatal("restore action did not return the synthetic message to Inbox")
	}
}

func TestMailAIComposeActionToolsUseRolltopComposeAndApproval(t *testing.T) {
	ctx := context.Background()
	server, user, _, _, _ := setupAutocryptComposeTest(t, ctx, false)
	accounts, err := server.store.ListMailAccountsForUser(ctx, user.ID)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("accounts=%d err=%v", len(accounts), err)
	}
	for name, role := range map[string]string{"Sent": "sent", "Drafts": "drafts"} {
		mailbox, err := server.store.GetOrCreateMailbox(ctx, user.ID, accounts[0].ID, name)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.store.UpdateMailboxSettings(ctx, user.ID, mailbox.ID, store.MailboxSettings{
			SyncMode: mailbox.SyncMode, Role: role, Icon: mailbox.Icon, ShowInSidebar: true,
			ShowInAllMail: mailbox.ShowInAllMail, IncludeInSearch: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/mail-ai/tools", nil)

	draftArgs := json.RawMessage(`{"to":"recipient@example.test","subject":"A9 draft","body":"Draft only"}`)
	draft, status, err := server.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "create_draft", Arguments: draftArgs})
	if err != nil || status != http.StatusOK {
		t.Fatalf("create_draft status=%d err=%v response=%+v", status, err, draft)
	}
	draftResult, ok := draft.Result.(map[string]any)
	if !ok || draftResult["send"] != false || draftResult["message_id"] == nil {
		t.Fatalf("create_draft result=%+v", draft.Result)
	}

	sendArgs := json.RawMessage(`{"to":"recipient@example.test","subject":"A9 send","body":"Approved send"}`)
	if _, status, err := server.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "send_mail", Arguments: sendArgs}); status != http.StatusConflict || err == nil {
		t.Fatalf("unconfirmed send status=%d err=%v", status, err)
	}
	sent, status, err := server.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "send_mail", Arguments: sendArgs, Confirmed: true})
	if err != nil || status != http.StatusOK {
		t.Fatalf("confirmed send status=%d err=%v response=%+v", status, err, sent)
	}
	sentResult, ok := sent.Result.(map[string]any)
	if !ok || sentResult["queued"] != true || sentResult["send_id"] == nil {
		t.Fatalf("confirmed send did not enter Rolltop outbox: %+v", sent.Result)
	}
}

func TestMailAIThreadEndpointPersistsVisibleMessagesAndRejectsCrossTenantWrite(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "thread-owner@example.invalid", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "thread-other@example.invalid", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st}

	getThread := func(user store.User) map[string]json.RawMessage {
		r := httptest.NewRequest(http.MethodGet, "/api/mail-ai/thread", nil)
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: user}))
		w := httptest.NewRecorder()
		s.handleAPI(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("GET thread=%d %s", w.Code, w.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	ownerThread := getThread(owner)
	var threadID string
	if err := json.Unmarshal(ownerThread["thread_id"], &threadID); err != nil || threadID == "" {
		t.Fatalf("thread id=%q err=%v", threadID, err)
	}
	putBody := `{"thread_id":"` + threadID + `","messages":[` +
		`{"id":"u1","role":"user","parts":[{"type":"text","text":"hello"}]},` +
		`{"id":"a1","role":"assistant","parts":[{"type":"reasoning","text":"private chain"},{"type":"text","text":"visible answer"},{"type":"tool-search_mail","toolCallId":"t1","state":"output-available","input":{"q":"x"},"output":[1]}]}` +
		`]}`
	put := httptest.NewRequest(http.MethodPut, "/api/mail-ai/thread", strings.NewReader(putBody))
	put = put.WithContext(context.WithValue(put.Context(), userContextKey, currentUser{User: owner}))
	put.AddCookie(&http.Cookie{Name: csrfCookie, Value: "thread-csrf"})
	put.Header.Set("X-CSRF-Token", s.csrfForBase("thread-csrf"))
	pw := httptest.NewRecorder()
	s.handleAPI(pw, put)
	if pw.Code != http.StatusOK {
		t.Fatalf("PUT thread=%d %s", pw.Code, pw.Body.String())
	}

	restored := getThread(owner)
	var messages []json.RawMessage
	if err := json.Unmarshal(restored["messages"], &messages); err != nil || len(messages) != 2 {
		t.Fatalf("restored messages=%d err=%v body=%s", len(messages), err, restored["messages"])
	}
	if strings.Contains(string(messages[1]), "private chain") || strings.Contains(string(messages[1]), `"type":"reasoning"`) {
		t.Fatalf("private reasoning persisted: %s", messages[1])
	}
	if !strings.Contains(string(messages[1]), "visible answer") || !strings.Contains(string(messages[1]), "tool-search_mail") {
		t.Fatalf("visible/tool result missing: %s", messages[1])
	}

	trace := httptest.NewRequest(http.MethodGet, "/api/mail-ai/trace?thread_id="+threadID, nil)
	trace = trace.WithContext(context.WithValue(trace.Context(), userContextKey, currentUser{User: owner}))
	tw := httptest.NewRecorder()
	s.handleAPI(tw, trace)
	if tw.Code != http.StatusOK {
		t.Fatalf("GET trace=%d %s", tw.Code, tw.Body.String())
	}
	if strings.Contains(tw.Body.String(), "private chain") || strings.Contains(tw.Body.String(), "reasoning") {
		t.Fatalf("private reasoning leaked in trace: %s", tw.Body.String())
	}
	if !strings.Contains(tw.Body.String(), `"tool_name":"search_mail"`) || !strings.Contains(tw.Body.String(), `"state":"output-available"`) || !strings.Contains(tw.Body.String(), `"q":"x"`) {
		t.Fatalf("persisted tool trace missing: %s", tw.Body.String())
	}

	crossTrace := httptest.NewRequest(http.MethodGet, "/api/mail-ai/trace?thread_id="+threadID, nil)
	crossTrace = crossTrace.WithContext(context.WithValue(crossTrace.Context(), userContextKey, currentUser{User: other}))
	ctw := httptest.NewRecorder()
	s.handleAPI(ctw, crossTrace)
	if ctw.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant trace=%d %s", ctw.Code, ctw.Body.String())
	}

	cross := httptest.NewRequest(http.MethodPut, "/api/mail-ai/thread", strings.NewReader(putBody))
	cross = cross.WithContext(context.WithValue(cross.Context(), userContextKey, currentUser{User: other}))
	cross.AddCookie(&http.Cookie{Name: csrfCookie, Value: "thread-csrf"})
	cross.Header.Set("X-CSRF-Token", s.csrfForBase("thread-csrf"))
	cw := httptest.NewRecorder()
	s.handleAPI(cw, cross)
	if cw.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant PUT=%d %s", cw.Code, cw.Body.String())
	}
}

func TestMailAIMessageSnapshotsStripReasoningParts(t *testing.T) {
	raw := []json.RawMessage{json.RawMessage(`{"id":"a1","role":"assistant","parts":[{"type":"reasoning","text":"secret"},{"type":"reasoning-start","id":"r1"},{"type":"text","text":"public"}]}`)}
	messages, err := mailAIMessageSnapshots(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || strings.Contains(string(messages[0].Payload), "secret") || strings.Contains(string(messages[0].Payload), "reasoning-start") || !strings.Contains(string(messages[0].Payload), "public") {
		t.Fatalf("sanitized message=%s", messages[0].Payload)
	}
}

func TestMailAIAgentPersistsRequestSnapshotBeforeProxy(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "agent-persist@example.invalid", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.CurrentMailAIThreadForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent" {
			t.Fatalf("sidecar path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer sidecar.Close()
	s := &Server{store: st, mailAIURL: sidecar.URL, mailAISharedToken: "persist-secret", mailAIHTTPClient: sidecar.Client()}
	body := `{"id":"sdk-ephemeral","thread_id":"` + thread.ID + `","messages":[{"id":"u1","role":"user","parts":[{"type":"text","text":"persist me"}]}]}`
	r := httptest.NewRequest(http.MethodPost, "/api/mail-ai/agent", strings.NewReader(body))
	r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: owner}))
	r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "agent-csrf"})
	r.Header.Set("X-CSRF-Token", s.csrfForBase("agent-csrf"))
	w := httptest.NewRecorder()
	s.handleAPI(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("agent=%d %s", w.Code, w.Body.String())
	}
	restored, err := st.CurrentMailAIThreadForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Messages) != 1 || !strings.Contains(string(restored.Messages[0]), "persist me") {
		t.Fatalf("agent request snapshot not persisted: %s", restored.Messages)
	}
}

func TestMailAIHistoryCreatesRestoresScopesAndAgentUsesStoredScope(t *testing.T) {
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
	owner, _ := st.CreateUser(ctx, "history-owner@example.invalid", "Owner", "hash", false)
	other, _ := st.CreateUser(ctx, "history-other@example.invalid", "Other", "hash", false)
	hub := mailhub.Service{Store: st, Blobs: blob.New(dir), Search: index}
	for _, account := range provider.MockAccounts(owner.ID) {
		if err := hub.Sync(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := hub.Messages(ctx, owner.ID, mailhub.Filter{})
	if err != nil || len(messages) < 2 {
		t.Fatalf("mock messages=%d err=%v", len(messages), err)
	}
	var receivedContext map[string]any
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body["context"], &receivedContext); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: ok\n\n"))
	}))
	defer sidecar.Close()
	s := &Server{store: st, blobs: hub.Blobs, search: index, mailAIURL: sidecar.URL, mailAISharedToken: "history-secret", mailAIHTTPClient: sidecar.Client()}
	withAuth := func(r *http.Request, user store.User) *http.Request {
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: user}))
		r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "history-csrf"})
		r.Header.Set("X-CSRF-Token", s.csrfForBase("history-csrf"))
		return r
	}
	createBody := `{"scope":"mail_thread","message_id":` + strconvInt64(messages[0].ID) + `}`
	create := withAuth(httptest.NewRequest(http.MethodPost, "/api/mail-ai/threads", strings.NewReader(createBody)), owner)
	cw := httptest.NewRecorder()
	s.handleAPI(cw, create)
	if cw.Code != http.StatusCreated {
		t.Fatalf("create thread=%d %s", cw.Code, cw.Body.String())
	}
	var created store.MailAIThreadSnapshot
	if err := json.Unmarshal(cw.Body.Bytes(), &created); err != nil || created.ID == "" || created.Scope != store.MailAIThreadScopeMailThread {
		t.Fatalf("created=%+v err=%v", created, err)
	}
	list := withAuth(httptest.NewRequest(http.MethodGet, "/api/mail-ai/threads", nil), owner)
	lw := httptest.NewRecorder()
	s.handleAPI(lw, list)
	if lw.Code != http.StatusOK || !strings.Contains(lw.Body.String(), created.ID) || !strings.Contains(lw.Body.String(), `"scope":"mail_thread"`) {
		t.Fatalf("history=%d %s", lw.Code, lw.Body.String())
	}
	restore := withAuth(httptest.NewRequest(http.MethodGet, "/api/mail-ai/thread?thread_id="+created.ID, nil), owner)
	rw := httptest.NewRecorder()
	s.handleAPI(rw, restore)
	if rw.Code != http.StatusOK || !strings.Contains(rw.Body.String(), created.ID) {
		t.Fatalf("restore=%d %s", rw.Code, rw.Body.String())
	}
	cross := withAuth(httptest.NewRequest(http.MethodGet, "/api/mail-ai/thread?thread_id="+created.ID, nil), other)
	xw := httptest.NewRecorder()
	s.handleAPI(xw, cross)
	if xw.Code != http.StatusNotFound {
		t.Fatalf("cross tenant restore=%d %s", xw.Code, xw.Body.String())
	}
	agentBody := `{"message_id":` + strconvInt64(messages[1].ID) + `,"thread_id":"` + created.ID + `","messages":[{"id":"u-scope","role":"user","parts":[{"type":"text","text":"stay in saved scope"}]}]}`
	agent := withAuth(httptest.NewRequest(http.MethodPost, "/api/mail-ai/agent", strings.NewReader(agentBody)), owner)
	aw := httptest.NewRecorder()
	s.handleAPI(aw, agent)
	if aw.Code != http.StatusOK {
		t.Fatalf("agent=%d %s", aw.Code, aw.Body.String())
	}
	if receivedContext["scope"] != store.MailAIThreadScopeMailThread {
		t.Fatalf("agent scope=%v", receivedContext["scope"])
	}
	gotMessageID, ok := receivedContext["message_id"].(float64)
	if !ok || int64(gotMessageID) != messages[0].ID {
		t.Fatalf("agent message_id=%v, want saved scope message %d", receivedContext["message_id"], messages[0].ID)
	}
}
