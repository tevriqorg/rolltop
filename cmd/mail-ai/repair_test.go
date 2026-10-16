package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"rolltop/backend/mailai"
)

func TestAgentRepairsMissingSearchQuery(t *testing.T) {
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"search","type":"function","function":{"name":"search_mail","arguments":"{}"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"找到了相关邮件。"}}]}`)
	}))
	defer model.Close()
	var got mailai.ToolRequest
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		writeJSON(w, 200, mailai.ToolResponse{Name: got.Name, Result: []any{}})
	}))
	defer hub.Close()

	s := &server{cfg: config{HubURL: hub.URL, Shared: "s", ModelURL: model.URL, Model: "m", HTTPClient: model.Client()}}
	if err := s.initModelConfig(); err != nil {
		t.Fatal(err)
	}
	input := mailai.ChatRequest{UserID: 1, Prompt: "找一下项目计划相关邮件", Context: mailai.CurrentMailContext{MessageID: 7, Account: "personal", ThreadID: "t1"}}
	body, _ := json.Marshal(input)
	r := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer s")
	w := httptest.NewRecorder()
	s.chat(w, r)
	if got.Name != "search_mail" || !strings.Contains(string(got.Arguments), "项目计划") {
		t.Fatalf("tool=%+v", got)
	}
	if !strings.Contains(w.Body.String(), "Search query was missing") || !strings.Contains(w.Body.String(), "找到了相关邮件") {
		t.Fatalf("stream=%s", w.Body.String())
	}
}

func TestAgentRecoversFromToolError(t *testing.T) {
	calls := 0
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","tool_calls":[{"id":"search","type":"function","function":{"name":"search_mail","arguments":"{\"q\":\"x\"}"}}]}}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"我换一种方式回答。"}}]}`)
	}))
	defer model.Close()
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"bad query"}`, http.StatusBadRequest)
	}))
	defer hub.Close()
	s := &server{cfg: config{HubURL: hub.URL, Shared: "s", ModelURL: model.URL, Model: "m", HTTPClient: model.Client()}}
	if err := s.initModelConfig(); err != nil {
		t.Fatal(err)
	}
	input := mailai.ChatRequest{UserID: 1, Prompt: "找邮件", Context: mailai.CurrentMailContext{MessageID: 1}}
	body, _ := json.Marshal(input)
	r := httptest.NewRequest(http.MethodPost, "/chat", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer s")
	w := httptest.NewRecorder()
	s.chat(w, r)
	got := w.Body.String()
	if !strings.Contains(got, `"type":"tool_error"`) || !strings.Contains(got, "我换一种方式回答") {
		t.Fatalf("stream=%s", got)
	}
}
