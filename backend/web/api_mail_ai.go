package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
				logHandlerError(r, fmt.Errorf("mail-ai stream: %w", readErr))
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
