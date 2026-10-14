package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"rolltop/backend/mailai"
)

func TestAgentUsesHubToolsAndStreamsDraftClosure(t *testing.T) {
	var mu sync.Mutex
	modelCalls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		modelCalls++
		w.Header().Set("Content-Type", "application/json")
		switch modelCalls {
		case 1:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-search","type":"function","function":{"name":"search_mail","arguments":"{\"q\":\"项目计划\"}"}}]}}]}`)
		case 2:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-draft","type":"function","function":{"name":"draft_reply","arguments":"{\"message_id\":1,\"body\":\"收到，我会查看。\"}"}}]}}]}`)
		default:
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"已生成回复草稿，不会自动发送。"}}]}`)
		}
	}))
	defer model.Close()

	var tools []mailai.ToolRequest
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gate-e-secret" {
			t.Fatalf("hub auth=%q", r.Header.Get("Authorization"))
		}
		var in mailai.ToolRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Fatal(err)
		}
		tools = append(tools, in)
		writeJSON(w, 200, mailai.ToolResponse{Name: in.Name, Result: map[string]any{"ok": true}})
	}))
	defer hub.Close()

	s := &server{cfg: config{HubURL: hub.URL, Shared: "gate-e-secret", ModelURL: model.URL, Model: "test-model", HTTPClient: model.Client()}}
	request := mailai.ChatRequest{UserID: 7, Prompt: "找一下项目计划，然后帮我写回复", Context: mailai.CurrentMailContext{Provider: "mock", Account: "personal", MessageID: 1, ThreadID: "project", Subject: "项目计划", From: "sender@example.invalid", Body: "你好"}}
	body, _ := json.Marshal(request)
	r := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer gate-e-secret")
	w := httptest.NewRecorder()
	s.chat(w, r)
	if w.Code != 200 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got := w.Body.String()
	for _, want := range []string{`"type":"context"`, `"tool":"search_mail"`, `"tool":"draft_reply"`, `"type":"tool_result"`, `"type":"answer_delta"`, `不会自动发送`, `"type":"done"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream missing %s:\n%s", want, got)
		}
	}
	if len(tools) != 2 || tools[0].Name != "search_mail" || tools[1].Name != "draft_reply" {
		t.Fatalf("tools=%+v", tools)
	}
}

func TestSideEffectRequiresSignedConfirmation(t *testing.T) {
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"call-label","type":"function","function":{"name":"add_label","arguments":"{\"message_id\":3,\"label\":\"follow-up\"}"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"标签修改需要你的确认。"}}]}`)
	}))
	defer model.Close()

	var confirmed mailai.ToolRequest
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&confirmed); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 200, mailai.ToolResponse{Name: confirmed.Name, Result: map[string]any{"ok": true}})
	}))
	defer hub.Close()

	s := &server{cfg: config{HubURL: hub.URL, Shared: "gate-e-secret", ModelURL: model.URL, Model: "test-model", HTTPClient: model.Client()}}
	input := mailai.ChatRequest{UserID: 9, Prompt: "加 follow-up 标签", Context: mailai.CurrentMailContext{MessageID: 3, Provider: "mock"}}
	data, _ := json.Marshal(input)
	r := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(data))
	r.Header.Set("Authorization", "Bearer gate-e-secret")
	w := httptest.NewRecorder()
	s.chat(w, r)
	if confirmed.Name != "" {
		t.Fatal("side effect executed before confirmation")
	}
	var token string
	for _, line := range strings.Split(w.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event mailai.Event
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event) == nil && event.Type == "confirmation_required" {
			token = event.ActionToken
		}
	}
	if token == "" {
		t.Fatalf("missing confirmation event: %s", w.Body.String())
	}
	confirmBody, _ := json.Marshal(mailai.ConfirmRequest{UserID: 9, ActionToken: token})
	cr := httptest.NewRequest(http.MethodPost, "/confirm", bytes.NewReader(confirmBody))
	cr.Header.Set("Authorization", "Bearer gate-e-secret")
	cw := httptest.NewRecorder()
	s.confirm(cw, cr)
	if cw.Code != 200 || confirmed.Name != "add_label" || !confirmed.Confirmed || confirmed.UserID != 9 {
		t.Fatalf("confirm status=%d tool=%+v body=%s", cw.Code, confirmed, cw.Body.String())
	}
}

func TestConfirmationExpiresAndHealthDoesNotExposeSecrets(t *testing.T) {
	s := &server{cfg: config{Shared: "never-leak", Model: "test-model"}}
	token, err := s.signAction(pendingAction{UserID: 1, Name: "mark_read", Arguments: json.RawMessage(`{"message_id":1,"read":true}`), Expires: time.Now().Add(-time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.verifyAction(token); err == nil {
		t.Fatal("expired token verified")
	}
	w := httptest.NewRecorder()
	s.health(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if strings.Contains(w.Body.String(), "never-leak") {
		t.Fatal("health leaked shared token")
	}
}
