package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/mailai"
	"rolltop/backend/mailhub"
	"rolltop/backend/provider"
	"rolltop/backend/store"
)

const mailAIMaxPromptBytes = 32 << 10

func (s *Server) apiMailAI(w http.ResponseWriter, r *http.Request, path string) {
	if path == "mail-ai/tools" {
		s.apiMailAITools(w, r)
		return
	}
	if path == "mail-ai/agent" {
		s.apiMailAIAgent(w, r)
		return
	}
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if path == "mail-ai/status" {
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		writeJSON(w, map[string]bool{"configured": s.mailAIURL != "" && s.mailAISharedToken != ""})
		return
	}
	if path == "mail-ai/threads" {
		s.apiMailAIThreads(w, r, cu)
		return
	}
	if path == "mail-ai/trace" {
		s.apiMailAITrace(w, r, cu)
		return
	}
	if path == "mail-ai/thread" {
		s.apiMailAIThread(w, r, cu)
		return
	}
	if path != "mail-ai/chat" && path != "mail-ai/confirm" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if s.mailAIURL == "" || s.mailAISharedToken == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is not configured")
		return
	}
	if path == "mail-ai/chat" {
		var body struct {
			MessageID int64  `json:"message_id"`
			Prompt    string `json:"prompt"`
		}
		if !decodeMailAIJSON(w, r, &body, mailAIMaxPromptBytes) || body.MessageID <= 0 || strings.TrimSpace(body.Prompt) == "" {
			if body.MessageID <= 0 || strings.TrimSpace(body.Prompt) == "" {
				writeAPIError(w, http.StatusBadRequest, "message_id and prompt are required")
			}
			return
		}
		context, err := s.mockMailAIContext(r.Context(), cu.User.ID, body.MessageID)
		if store.IsNotFound(err) {
			writeAPIError(w, http.StatusNotFound, "mock message not found")
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		s.proxyMailAI(w, r, "/chat", mailai.ChatRequest{UserID: cu.User.ID, Prompt: strings.TrimSpace(body.Prompt), Context: context}, true)
		return
	}
	var body struct {
		ActionToken string `json:"action_token"`
	}
	if !decodeMailAIJSON(w, r, &body, 24<<10) || strings.TrimSpace(body.ActionToken) == "" {
		if strings.TrimSpace(body.ActionToken) == "" {
			writeAPIError(w, http.StatusBadRequest, "action_token is required")
		}
		return
	}
	s.proxyMailAI(w, r, "/confirm", mailai.ConfirmRequest{UserID: cu.User.ID, ActionToken: body.ActionToken}, false)
}

type mailAIThreadRequest struct {
	ThreadID string            `json:"thread_id"`
	Messages []json.RawMessage `json:"messages"`
}

type mailAIThreadCreateRequest struct {
	Scope     string `json:"scope"`
	MessageID int64  `json:"message_id"`
}

func (s *Server) apiMailAIThreads(w http.ResponseWriter, r *http.Request, cu currentUser) {
	switch r.Method {
	case http.MethodGet:
		threads, err := s.store.ListMailAIThreadsForUser(r.Context(), cu.User.ID, 50)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		writeJSON(w, map[string]any{"threads": threads})
	case http.MethodPost:
		if !s.verifyCSRF(w, r) {
			return
		}
		var body mailAIThreadCreateRequest
		if !decodeMailAIJSON(w, r, &body, 16<<10) {
			return
		}
		spec, err := s.mailAIThreadScopeSpec(r.Context(), cu.User.ID, body.Scope, body.MessageID)
		if err != nil {
			if store.IsNotFound(err) {
				writeAPIError(w, http.StatusNotFound, "mail-ai scope message not found")
			} else {
				writeAPIError(w, http.StatusBadRequest, err.Error())
			}
			return
		}
		thread, err := s.store.CreateMailAIThreadForUser(r.Context(), cu.User.ID, spec)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		w.WriteHeader(http.StatusCreated)
		writeJSON(w, thread)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) apiMailAIThread(w http.ResponseWriter, r *http.Request, cu currentUser) {
	switch r.Method {
	case http.MethodGet:
		var thread store.MailAIThreadSnapshot
		var err error
		if threadID := strings.TrimSpace(r.URL.Query().Get("thread_id")); threadID != "" {
			thread, err = s.store.MailAIThreadForUser(r.Context(), cu.User.ID, threadID)
		} else {
			messageID, parseErr := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("message_id")), 10, 64)
			if r.URL.Query().Get("message_id") == "" {
				messageID = 0
			} else if parseErr != nil || messageID <= 0 {
				writeAPIError(w, http.StatusBadRequest, "invalid message_id")
				return
			}
			spec, specErr := s.mailAIThreadScopeSpec(r.Context(), cu.User.ID, r.URL.Query().Get("scope"), messageID)
			if specErr != nil {
				if store.IsNotFound(specErr) {
					writeAPIError(w, http.StatusNotFound, "mail-ai scope message not found")
				} else {
					writeAPIError(w, http.StatusBadRequest, specErr.Error())
				}
				return
			}
			thread, err = s.store.CurrentMailAIThreadForScopeForUser(r.Context(), cu.User.ID, spec)
		}
		if store.IsNotFound(err) {
			writeAPIError(w, http.StatusNotFound, "mail-ai thread not found")
			return
		}
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		writeJSON(w, thread)
	case http.MethodPut:
		if !s.verifyCSRF(w, r) {
			return
		}
		var body mailAIThreadRequest
		if !decodeMailAIJSON(w, r, &body, 2<<20) {
			return
		}
		messages, err := mailAIMessageSnapshots(body.Messages)
		if err != nil || strings.TrimSpace(body.ThreadID) == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid mail-ai thread snapshot")
			return
		}
		if err := s.store.SaveMailAIThreadMessagesForUser(r.Context(), cu.User.ID, body.ThreadID, messages); err != nil {
			if store.IsNotFound(err) {
				writeAPIError(w, http.StatusNotFound, "mail-ai thread not found")
				return
			}
			s.serverError(w, r, err)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	default:
		methodNotAllowed(w)
	}
}

type mailAITraceEvent struct {
	ID         string          `json:"id"`
	MessageID  string          `json:"message_id"`
	ToolCallID string          `json:"tool_call_id"`
	ToolName   string          `json:"tool_name"`
	State      string          `json:"state"`
	Input      json.RawMessage `json:"input,omitempty"`
	Output     json.RawMessage `json:"output,omitempty"`
	ErrorText  string          `json:"error_text,omitempty"`
}

func (s *Server) apiMailAITrace(w http.ResponseWriter, r *http.Request, cu currentUser) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	threadID := strings.TrimSpace(r.URL.Query().Get("thread_id"))
	if threadID == "" {
		writeAPIError(w, http.StatusBadRequest, "mail-ai thread id is required")
		return
	}
	thread, err := s.store.MailAIThreadForUser(r.Context(), cu.User.ID, threadID)
	if store.IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "mail-ai thread not found")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	events, err := mailAITraceEvents(thread.Messages)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{"thread_id": thread.ID, "events": events})
}

func mailAITraceEvents(messages []json.RawMessage) ([]mailAITraceEvent, error) {
	events := make([]mailAITraceEvent, 0)
	for _, raw := range messages {
		var message struct {
			ID    string            `json:"id"`
			Role  string            `json:"role"`
			Parts []json.RawMessage `json:"parts"`
		}
		if err := json.Unmarshal(raw, &message); err != nil {
			return nil, err
		}
		if message.Role != "assistant" {
			continue
		}
		for index, rawPart := range message.Parts {
			var part struct {
				Type       string          `json:"type"`
				ToolName   string          `json:"toolName"`
				ToolCallID string          `json:"toolCallId"`
				State      string          `json:"state"`
				Input      json.RawMessage `json:"input"`
				Output     json.RawMessage `json:"output"`
				ErrorText  string          `json:"errorText"`
			}
			if err := json.Unmarshal(rawPart, &part); err != nil {
				return nil, err
			}
			toolName := strings.TrimSpace(part.ToolName)
			if strings.HasPrefix(part.Type, "tool-") {
				toolName = strings.TrimPrefix(part.Type, "tool-")
			} else if part.Type != "dynamic-tool" {
				continue
			}
			if toolName == "" {
				continue
			}
			id := strings.TrimSpace(part.ToolCallID)
			if id == "" {
				id = fmt.Sprintf("%s:%d", message.ID, index)
			}
			events = append(events, mailAITraceEvent{
				ID: id, MessageID: message.ID, ToolCallID: part.ToolCallID, ToolName: toolName,
				State: part.State, Input: part.Input, Output: part.Output, ErrorText: part.ErrorText,
			})
		}
	}
	return events, nil
}

func (s *Server) mailAIThreadScopeSpec(ctx context.Context, userID int64, scope string, messageID int64) (store.MailAIThreadScopeSpec, error) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		if messageID > 0 {
			scope = store.MailAIThreadScopeMailThread
		} else {
			scope = store.MailAIThreadScopeGlobal
		}
	}
	if scope == store.MailAIThreadScopeGlobal {
		return store.MailAIThreadScopeSpec{Kind: scope, Key: scope, Context: json.RawMessage(`{"mode":"mailbox","scope":"global"}`)}, nil
	}
	if messageID <= 0 {
		return store.MailAIThreadScopeSpec{}, errors.New("message_id is required for this mail-ai scope")
	}
	mailContext, err := s.mockMailAIContext(ctx, userID, messageID)
	if err != nil {
		return store.MailAIThreadScopeSpec{}, err
	}
	if scope != store.MailAIThreadScopeMessage && scope != store.MailAIThreadScopeMailThread {
		return store.MailAIThreadScopeSpec{}, errors.New("invalid mail-ai scope")
	}
	contextValue := map[string]any{
		"mode": "message", "scope": scope, "provider": mailContext.Provider, "account": mailContext.Account,
		"message_id": mailContext.MessageID, "mail_thread_id": mailContext.ThreadID, "subject": mailContext.Subject,
		"from": mailContext.From, "to": mailContext.To, "cc": mailContext.CC, "date": mailContext.Date,
	}
	contextJSON, err := json.Marshal(contextValue)
	if err != nil {
		return store.MailAIThreadScopeSpec{}, err
	}
	key := strconv.FormatInt(mailContext.MessageID, 10)
	if scope == store.MailAIThreadScopeMailThread {
		keyJSON, _ := json.Marshal([]string{mailContext.Account, mailContext.ThreadID})
		key = string(keyJSON)
	}
	return store.MailAIThreadScopeSpec{Kind: scope, Key: key, Context: contextJSON}, nil
}

func (s *Server) mailAIAgentContextForThread(ctx context.Context, userID int64, thread store.MailAIThreadSnapshot) (map[string]any, error) {
	if thread.Scope == store.MailAIThreadScopeGlobal {
		return map[string]any{"mode": "mailbox", "scope": store.MailAIThreadScopeGlobal}, nil
	}
	var saved struct {
		MessageID    int64  `json:"message_id"`
		Account      string `json:"account"`
		MailThreadID string `json:"mail_thread_id"`
	}
	if err := json.Unmarshal(thread.Context, &saved); err != nil {
		return nil, err
	}
	messageID := saved.MessageID
	if thread.Scope == store.MailAIThreadScopeMailThread {
		mailContext, err := s.mockMailAIContext(ctx, userID, messageID)
		if err != nil || mailContext.Account != saved.Account || mailContext.ThreadID != saved.MailThreadID {
			messageID = 0
			meta, listErr := s.store.ProviderMessages(ctx, userID)
			if listErr != nil {
				return nil, listErr
			}
			for _, item := range meta {
				if item.Account == saved.Account && item.ThreadID == saved.MailThreadID {
					messageID = item.ID
					break
				}
			}
		}
	}
	if messageID <= 0 {
		return nil, store.ErrNotFound
	}
	mailContext, err := s.mockMailAIContext(ctx, userID, messageID)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(mailContext)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	out["mode"] = "message"
	out["scope"] = thread.Scope
	return out, nil
}

func mailAIMessageSnapshots(rawMessages []json.RawMessage) ([]store.MailAIMessageSnapshot, error) {
	messages := make([]store.MailAIMessageSnapshot, 0, len(rawMessages))
	for _, raw := range rawMessages {
		var object map[string]json.RawMessage
		if !json.Valid(raw) || json.Unmarshal(raw, &object) != nil {
			return nil, errors.New("invalid message")
		}
		var id, role string
		if json.Unmarshal(object["id"], &id) != nil || json.Unmarshal(object["role"], &role) != nil || strings.TrimSpace(id) == "" || strings.TrimSpace(role) == "" {
			return nil, errors.New("message id and role are required")
		}
		if rawParts := object["parts"]; len(rawParts) > 0 {
			var parts []json.RawMessage
			if json.Unmarshal(rawParts, &parts) != nil {
				return nil, errors.New("invalid message parts")
			}
			visible := make([]json.RawMessage, 0, len(parts))
			for _, part := range parts {
				var header struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(part, &header) != nil {
					return nil, errors.New("invalid message part")
				}
				if header.Type == "reasoning" || strings.HasPrefix(header.Type, "reasoning-") {
					continue
				}
				visible = append(visible, part)
			}
			filtered, err := json.Marshal(visible)
			if err != nil {
				return nil, err
			}
			object["parts"] = filtered
		}
		payload, err := json.Marshal(object)
		if err != nil {
			return nil, err
		}
		messages = append(messages, store.MailAIMessageSnapshot{ID: strings.TrimSpace(id), Role: strings.TrimSpace(role), Payload: payload})
	}
	return messages, nil
}

func (s *Server) apiMailAIAgent(w http.ResponseWriter, r *http.Request) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if s.mailAIURL == "" || s.mailAISharedToken == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 512<<10)
	defer r.Body.Close()
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid agent request")
		return
	}
	// Browser page context is only a positioning hint when a thread is created.
	// Once a thread exists, its server-persisted scope is authoritative.
	delete(body, "message_id")
	if !json.Valid(body["messages"]) {
		writeAPIError(w, http.StatusBadRequest, "messages are required")
		return
	}
	var threadID string
	if raw := body["thread_id"]; len(raw) == 0 || json.Unmarshal(raw, &threadID) != nil || strings.TrimSpace(threadID) == "" {
		writeAPIError(w, http.StatusBadRequest, "mail-ai thread id is required")
		return
	}
	var rawMessages []json.RawMessage
	if json.Unmarshal(body["messages"], &rawMessages) != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid messages")
		return
	}
	messages, err := mailAIMessageSnapshots(rawMessages)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid messages")
		return
	}
	thread, err := s.store.MailAIThreadForUser(r.Context(), cu.User.ID, threadID)
	if store.IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "mail-ai thread not found")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if err := s.store.SaveMailAIThreadMessagesForUser(r.Context(), cu.User.ID, threadID, messages); err != nil {
		if store.IsNotFound(err) {
			writeAPIError(w, http.StatusNotFound, "mail-ai thread not found")
			return
		}
		s.serverError(w, r, err)
		return
	}
	contextValue, err := s.mailAIAgentContextForThread(r.Context(), cu.User.ID, thread)
	if store.IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "mail-ai thread context is no longer available")
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	userRaw, _ := json.Marshal(cu.User.ID)
	contextRaw, _ := json.Marshal(contextValue)
	body["user_id"] = userRaw
	body["context"] = contextRaw
	encoded, err := json.Marshal(body)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.mailAIURL+"/agent", bytes.NewReader(encoded))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.mailAISharedToken)
	res, err := s.mailAIHTTPClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is unavailable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		writeAPIError(w, http.StatusBadGateway, strings.TrimSpace(string(message)))
		return
	}
	for _, key := range []string{"Content-Type", "Cache-Control", "X-Vercel-AI-UI-Message-Stream"} {
		if value := res.Header.Get(key); value != "" {
			w.Header().Set(key, value)
		}
	}
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 8192)
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				logHandlerError(r, fmt.Errorf("mail-ai agent stream read failure error_type=%T", readErr))
			}
			return
		}
	}
}

func decodeMailAIJSON(w http.ResponseWriter, r *http.Request, out any, max int64) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid mail-ai request")
		return false
	}
	return true
}

func (s *Server) proxyMailAI(w http.ResponseWriter, r *http.Request, path string, payload any, stream bool) {
	body, err := json.Marshal(payload)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.mailAIURL+path, bytes.NewReader(body))
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.mailAISharedToken)
	res, err := s.mailAIHTTPClient.Do(req)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is unavailable")
		return
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		writeAPIError(w, http.StatusBadGateway, strings.TrimSpace(string(message)))
		return
	}
	if !stream {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.Copy(w, io.LimitReader(res.Body, 1<<20))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := res.Body.Read(buf)
		if n > 0 {
			_, _ = w.Write(buf[:n])
			if flusher != nil {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				logHandlerError(r, fmt.Errorf("mail-ai stream read failure error_type=%T", readErr))
			}
			return
		}
	}
}

func (s *Server) apiMailAITools(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.mailAISharedToken == "" || !constantBearer(r.Header.Get("Authorization"), s.mailAISharedToken) {
		writeAPIError(w, http.StatusUnauthorized, "mail-ai token required")
		return
	}
	var in mailai.ToolRequest
	if !decodeMailAIJSON(w, r, &in, 64<<10) || in.UserID <= 0 || strings.TrimSpace(in.Name) == "" {
		if in.UserID <= 0 || strings.TrimSpace(in.Name) == "" {
			writeAPIError(w, http.StatusBadRequest, "invalid tool request")
		}
		return
	}
	result, status, err := s.runMailAITool(r, in)
	if err != nil {
		writeAPIError(w, status, err.Error())
		return
	}
	writeJSON(w, result)
}

func constantBearer(header, want string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	return len(got) == len(want) && subtleConstantCompare(got, want)
}

func subtleConstantCompare(a, b string) bool {
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (s *Server) runMailAITool(r *http.Request, in mailai.ToolRequest) (mailai.ToolResponse, int, error) {
	hub := mailhub.Service{Store: s.store, Blobs: s.blobs, Search: s.search}
	switch in.Name {
	case "list_mail":
		var args struct {
			Account string `json:"account"`
			Limit   int    `json:"limit"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil {
			return mailai.ToolResponse{}, 400, errors.New("invalid list_mail arguments")
		}
		if args.Limit <= 0 || args.Limit > 20 {
			args.Limit = 20
		}
		messages, err := hub.Messages(r.Context(), in.UserID, mailhub.Filter{Account: strings.TrimSpace(args.Account)})
		if err != nil {
			return mailai.ToolResponse{}, 500, err
		}
		if len(messages) > args.Limit {
			messages = messages[:args.Limit]
		}
		items := make([]map[string]any, 0, len(messages))
		for _, m := range messages {
			ctx, err := s.mockMailAIContext(r.Context(), in.UserID, m.ID)
			if err != nil {
				return mailai.ToolResponse{}, 500, err
			}
			items = append(items, map[string]any{
				"message_id": ctx.MessageID, "account": ctx.Account, "thread_id": ctx.ThreadID,
				"subject": ctx.Subject, "from": ctx.From, "date": ctx.Date,
			})
		}
		return mailai.ToolResponse{Name: in.Name, Result: items}, 200, nil
	case "search_mail":
		var args struct {
			Q string `json:"q"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || strings.TrimSpace(args.Q) == "" {
			return mailai.ToolResponse{}, 400, errors.New("search_mail requires q")
		}
		messages, err := hub.Messages(r.Context(), in.UserID, mailhub.Filter{Query: strings.TrimSpace(args.Q)})
		if err != nil {
			return mailai.ToolResponse{}, 500, err
		}
		if len(messages) > 8 {
			messages = messages[:8]
		}
		items := make([]mailai.CurrentMailContext, 0, len(messages))
		for _, m := range messages {
			context, err := s.mockMailAIContext(r.Context(), in.UserID, m.ID)
			if err != nil {
				return mailai.ToolResponse{}, 500, err
			}
			items = append(items, context)
		}
		return mailai.ToolResponse{Name: in.Name, Result: items}, 200, nil
	case "get_message":
		var args struct {
			MessageID int64 `json:"message_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
			return mailai.ToolResponse{}, 400, errors.New("get_message requires message_id")
		}
		context, err := s.mockMailAIContext(r.Context(), in.UserID, args.MessageID)
		if store.IsNotFound(err) {
			return mailai.ToolResponse{}, 404, errors.New("mock message not found")
		}
		if err != nil {
			return mailai.ToolResponse{}, 500, err
		}
		return mailai.ToolResponse{Name: in.Name, Result: context}, 200, nil
	case "get_thread":
		var args struct {
			Account  string `json:"account"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || strings.TrimSpace(args.Account) == "" || strings.TrimSpace(args.ThreadID) == "" {
			return mailai.ToolResponse{}, 400, errors.New("get_thread requires account and thread_id")
		}
		meta, err := s.store.ProviderMessages(r.Context(), in.UserID)
		if err != nil {
			return mailai.ToolResponse{}, 500, err
		}
		items := []mailai.CurrentMailContext{}
		for _, m := range meta {
			if m.Account != args.Account || m.ThreadID != args.ThreadID {
				continue
			}
			context, err := s.mockMailAIContext(r.Context(), in.UserID, m.ID)
			if err != nil {
				return mailai.ToolResponse{}, 500, err
			}
			items = append(items, context)
		}
		return mailai.ToolResponse{Name: in.Name, Result: items}, 200, nil
	case "draft_reply":
		var args struct {
			MessageID int64  `json:"message_id"`
			Body      string `json:"body"`
		}
		if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 || strings.TrimSpace(args.Body) == "" {
			return mailai.ToolResponse{}, 400, errors.New("draft_reply requires message_id and body")
		}
		context, err := s.mockMailAIContext(r.Context(), in.UserID, args.MessageID)
		if err != nil {
			return mailai.ToolResponse{}, 404, errors.New("mock message not found")
		}
		subject := context.Subject
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
			subject = "Re: " + subject
		}
		return mailai.ToolResponse{Name: in.Name, Result: map[string]any{"to": context.From, "subject": subject, "body": strings.TrimSpace(args.Body), "send": false}}, 200, nil
	case "add_label", "archive_message", "mark_read":
		if !in.Confirmed {
			return mailai.ToolResponse{}, http.StatusConflict, errors.New("explicit confirmation required")
		}
		return s.runConfirmedMockMutation(r, in, hub)
	default:
		return mailai.ToolResponse{}, 400, errors.New("unsupported mail-ai tool")
	}
}

func (s *Server) runConfirmedMockMutation(r *http.Request, in mailai.ToolRequest, hub mailhub.Service) (mailai.ToolResponse, int, error) {
	var args struct {
		MessageID int64  `json:"message_id"`
		Label     string `json:"label"`
		Read      bool   `json:"read"`
	}
	if err := json.Unmarshal(in.Arguments, &args); err != nil || args.MessageID <= 0 {
		return mailai.ToolResponse{}, 400, errors.New("message_id is required")
	}
	meta, err := s.mockProviderMessage(r.Context(), in.UserID, args.MessageID)
	if store.IsNotFound(err) {
		return mailai.ToolResponse{}, 404, errors.New("mock message not found")
	}
	if err != nil {
		return mailai.ToolResponse{}, 500, err
	}
	change := provider.Mutation{MessageID: meta.NativeID}
	switch in.Name {
	case "add_label":
		if strings.TrimSpace(args.Label) == "" {
			return mailai.ToolResponse{}, 400, errors.New("label is required")
		}
		change.Action, change.Value, change.Label = "label", true, strings.TrimSpace(args.Label)
	case "archive_message":
		change.Action = "archive"
	case "mark_read":
		change.Action, change.Value = "read", args.Read
	}
	if err := hub.Mutate(r.Context(), provider.Account{UserID: in.UserID, ID: meta.Account, Provider: provider.Mock}, change); err != nil {
		return mailai.ToolResponse{}, 500, err
	}
	s.noteMailListChanged(in.UserID)
	return mailai.ToolResponse{Name: in.Name, Result: map[string]any{"ok": true, "message_id": args.MessageID}}, 200, nil
}

func (s *Server) mockProviderMessage(ctx context.Context, userID, messageID int64) (store.ProviderMessage, error) {
	meta, err := s.store.ProviderMessages(ctx, userID)
	if err != nil {
		return store.ProviderMessage{}, err
	}
	for _, m := range meta {
		if m.ID == messageID {
			return m, nil
		}
	}
	return store.ProviderMessage{}, store.ErrNotFound
}

func (s *Server) mockMailAIContext(ctx context.Context, userID, messageID int64) (mailai.CurrentMailContext, error) {
	meta, err := s.mockProviderMessage(ctx, userID, messageID)
	if err != nil {
		return mailai.CurrentMailContext{}, err
	}
	row, err := s.store.GetMessageForUser(ctx, userID, messageID)
	if err != nil {
		return mailai.CurrentMailContext{}, err
	}
	body := row.BodyText
	if len(body) > 12<<10 {
		body = body[:12<<10] + "…"
	}
	return mailai.CurrentMailContext{
		Provider: "mock", Account: meta.Account, MessageID: row.ID, NativeID: meta.NativeID, ThreadID: meta.ThreadID,
		Subject: row.Subject, From: row.FromAddr, To: row.ToAddr, CC: row.CCAddr, Date: row.Date.UTC().Format(time.RFC3339), Body: body,
	}, nil
}
