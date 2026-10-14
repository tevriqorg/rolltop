package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"rolltop/backend/mailai"
)

type config struct {
	Addr            string
	HubURL          string
	Shared          string
	ModelURL        string
	ModelKey        string
	Model           string
	ModelConfigFile string
	HTTPClient      *http.Client
}

type server struct {
	cfg   config
	model modelRuntime
}

type modelMessage struct {
	Role       string          `json:"role"`
	Content    string          `json:"content,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []modelToolCall `json:"tool_calls,omitempty"`
}

type modelToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type completionResponse struct {
	Choices []struct {
		Message modelMessage `json:"message"`
	} `json:"choices"`
}

type pendingAction struct {
	UserID    int64           `json:"user_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Expires   int64           `json:"expires"`
}

func main() {
	cfg := config{
		Addr:            env("MAIL_AI_ADDR", ":8090"),
		HubURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("MAIL_AI_HUB_URL")), "/"),
		Shared:          strings.TrimSpace(os.Getenv("MAIL_AI_SHARED_TOKEN")),
		ModelURL:        strings.TrimRight(strings.TrimSpace(os.Getenv("MAIL_AI_MODEL_BASE_URL")), "/"),
		ModelKey:        strings.TrimSpace(os.Getenv("MAIL_AI_MODEL_API_KEY")),
		Model:           strings.TrimSpace(os.Getenv("MAIL_AI_MODEL")),
		ModelConfigFile: strings.TrimSpace(os.Getenv("MAIL_AI_MODEL_CONFIG_FILE")),
		HTTPClient:      &http.Client{Timeout: 90 * time.Second},
	}
	if err := validateConfig(cfg); err != nil {
		log.Fatal(err)
	}
	s := &server{cfg: cfg}
	if err := s.initModelConfig(); err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/config", s.modelConfigHandler)
	mux.HandleFunc("/chat", s.chat)
	mux.HandleFunc("/confirm", s.confirm)
	log.Printf("mail-ai listening on %s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func validateConfig(c config) error {
	if c.Shared == "" {
		return errors.New("MAIL_AI_SHARED_TOKEN is required")
	}
	if c.HubURL == "" {
		return errors.New("MAIL_AI_HUB_URL is required")
	}
	if (c.ModelURL == "") != (c.Model == "") {
		return errors.New("MAIL_AI_MODEL_BASE_URL and MAIL_AI_MODEL must be set together")
	}
	if c.ModelURL != "" {
		if _, _, err := normalizeModelConfig(c.ModelURL, c.Model); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", 405)
		return
	}
	baseURL, _, model := s.currentModelConfig()
	writeJSON(w, 200, map[string]any{"ok": true, "configured": baseURL != "" && model != "", "model": model})
}

func (s *server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	value := r.Header.Get("Authorization")
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	got := []byte(strings.TrimSpace(strings.TrimPrefix(value, prefix)))
	want := []byte(s.cfg.Shared)
	return len(got) == len(want) && hmac.Equal(got, want)
}

func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	baseURL, _, model := s.currentModelConfig()
	if baseURL == "" || model == "" {
		http.Error(w, "model is not configured", http.StatusServiceUnavailable)
		return
	}
	var in mailai.ChatRequest
	if err := decodeLimited(r, &in, 64<<10); err != nil || in.UserID <= 0 || strings.TrimSpace(in.Prompt) == "" || in.Context.MessageID <= 0 {
		http.Error(w, "invalid chat request", 400)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", 500)
		return
	}
	emit := func(event mailai.Event) {
		data, _ := json.Marshal(event)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	emit(mailai.Event{Type: "context", Result: in.Context})
	if err := s.runAgent(r.Context(), in, emit); err != nil {
		emit(mailai.Event{Type: "error", Text: safeError(err)})
	}
	emit(mailai.Event{Type: "done"})
}

func (s *server) confirm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if !s.authorized(r) {
		http.Error(w, "unauthorized", 401)
		return
	}
	var in mailai.ConfirmRequest
	if err := decodeLimited(r, &in, 16<<10); err != nil || in.UserID <= 0 || in.ActionToken == "" {
		http.Error(w, "invalid confirmation", 400)
		return
	}
	action, err := s.verifyAction(in.ActionToken)
	if err != nil || action.UserID != in.UserID {
		http.Error(w, "invalid or expired action", 400)
		return
	}
	result, err := s.callHubTool(r.Context(), mailai.ToolRequest{UserID: action.UserID, Name: action.Name, Arguments: action.Arguments, Confirmed: true})
	if err != nil {
		http.Error(w, safeError(err), 502)
		return
	}
	writeJSON(w, 200, result)
}

func (s *server) runAgent(ctx context.Context, in mailai.ChatRequest, emit func(mailai.Event)) error {
	messages := []modelMessage{{Role: "system", Content: systemPrompt(in.Context)}, {Role: "user", Content: in.Prompt}}
	for turn := 0; turn < 6; turn++ {
		msg, err := s.complete(ctx, messages)
		if err != nil {
			return err
		}
		messages = append(messages, msg)
		if len(msg.ToolCalls) == 0 {
			if strings.TrimSpace(msg.Content) == "" {
				return errors.New("model returned no answer")
			}
			streamText(msg.Content, func(chunk string) { emit(mailai.Event{Type: "answer_delta", Text: chunk}) })
			return nil
		}
		for _, call := range msg.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			if !json.Valid(args) {
				args = json.RawMessage(`{}`)
			}
			var visible any
			_ = json.Unmarshal(args, &visible)
			emit(mailai.Event{Type: "tool_call", Tool: call.Function.Name, Arguments: visible})
			if isSideEffect(call.Function.Name) {
				token, err := s.signAction(pendingAction{UserID: in.UserID, Name: call.Function.Name, Arguments: args, Expires: time.Now().Add(10 * time.Minute).Unix()})
				if err != nil {
					return err
				}
				emit(mailai.Event{Type: "confirmation_required", Tool: call.Function.Name, Arguments: visible, ActionToken: token})
				messages = append(messages, modelMessage{Role: "tool", ToolCallID: call.ID, Content: `{"status":"confirmation_required"}`})
				continue
			}
			result, err := s.callHubTool(ctx, mailai.ToolRequest{UserID: in.UserID, Name: call.Function.Name, Arguments: args})
			if err != nil {
				return err
			}
			emit(mailai.Event{Type: "tool_result", Tool: call.Function.Name, Result: result.Result})
			payload, _ := json.Marshal(result)
			messages = append(messages, modelMessage{Role: "tool", ToolCallID: call.ID, Content: string(payload)})
		}
	}
	return errors.New("mail-ai reached tool-call limit")
}

func systemPrompt(c mailai.CurrentMailContext) string {
	ctx, _ := json.Marshal(c)
	return "You are Mail AI. Use tools when needed. Never claim an action was performed unless a tool result says so. " +
		"For reply requests, draft text first and call draft_reply; never send mail. Mutating tools require user confirmation. " +
		"Current structured mail context: " + string(ctx)
}

func (s *server) complete(ctx context.Context, messages []modelMessage) (modelMessage, error) {
	baseURL, apiKey, model := s.currentModelConfig()
	if baseURL == "" || model == "" {
		return modelMessage{}, errors.New("model is not configured")
	}
	payload := map[string]any{"model": model, "messages": messages, "tools": toolDefinitions(), "tool_choice": "auto", "stream": false}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return modelMessage{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	res, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return modelMessage{}, fmt.Errorf("model endpoint: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return modelMessage{}, fmt.Errorf("model endpoint status %d: %s", res.StatusCode, strings.TrimSpace(string(limited)))
	}
	var out completionResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&out); err != nil {
		return modelMessage{}, err
	}
	if len(out.Choices) == 0 {
		return modelMessage{}, errors.New("model endpoint returned no choices")
	}
	return out.Choices[0].Message, nil
}

func toolDefinitions() []map[string]any {
	def := func(name, desc string, props map[string]any, required ...string) map[string]any {
		return map[string]any{"type": "function", "function": map[string]any{"name": name, "description": desc, "parameters": map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}}}
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	integer := func(desc string) map[string]any { return map[string]any{"type": "integer", "description": desc} }
	boolean := func(desc string) map[string]any { return map[string]any{"type": "boolean", "description": desc} }
	return []map[string]any{
		def("search_mail", "Search the signed-in user's Mock Mail Hub", map[string]any{"q": str("Search query")}, "q"),
		def("get_message", "Read one Mock message by local message ID", map[string]any{"message_id": integer("Local message ID")}, "message_id"),
		def("get_thread", "Read one Mock account conversation by thread ID", map[string]any{"account": str("Source account ID from context/search results"), "thread_id": str("Provider thread ID")}, "account", "thread_id"),
		def("draft_reply", "Create a non-sending reply draft object", map[string]any{"message_id": integer("Message being replied to"), "body": str("Draft reply body")}, "message_id", "body"),
		def("add_label", "Add a label to a Mock message; requires confirmation", map[string]any{"message_id": integer("Local message ID"), "label": str("Label")}, "message_id", "label"),
		def("archive_message", "Archive a Mock message; requires confirmation", map[string]any{"message_id": integer("Local message ID")}, "message_id"),
		def("mark_read", "Set Mock message read state; requires confirmation", map[string]any{"message_id": integer("Local message ID"), "read": boolean("Desired read state")}, "message_id", "read"),
	}
}

func isSideEffect(name string) bool {
	return name == "add_label" || name == "archive_message" || name == "mark_read"
}

func (s *server) callHubTool(ctx context.Context, in mailai.ToolRequest) (mailai.ToolResponse, error) {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.HubURL+"/api/mail-ai/tools", bytes.NewReader(body))
	if err != nil {
		return mailai.ToolResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.cfg.Shared)
	res, err := s.cfg.HTTPClient.Do(req)
	if err != nil {
		return mailai.ToolResponse{}, fmt.Errorf("mail hub tool: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		limited, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return mailai.ToolResponse{}, fmt.Errorf("mail hub tool status %d: %s", res.StatusCode, strings.TrimSpace(string(limited)))
	}
	var out mailai.ToolResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return out, err
	}
	return out, nil
}

func (s *server) signAction(action pendingAction) (string, error) {
	payload, err := json.Marshal(action)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.Shared))
	_, _ = mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func (s *server) verifyAction(token string) (pendingAction, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return pendingAction{}, errors.New("bad token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return pendingAction{}, err
	}
	got, err := hex.DecodeString(parts[1])
	if err != nil {
		return pendingAction{}, err
	}
	mac := hmac.New(sha256.New, []byte(s.cfg.Shared))
	_, _ = mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return pendingAction{}, errors.New("bad signature")
	}
	var action pendingAction
	if err := json.Unmarshal(payload, &action); err != nil {
		return action, err
	}
	if action.Expires < time.Now().Unix() || !isSideEffect(action.Name) {
		return pendingAction{}, errors.New("expired action")
	}
	return action, nil
}

func streamText(text string, emit func(string)) {
	const target = 48
	var b strings.Builder
	for len(text) > 0 {
		r, size := utf8.DecodeRuneInString(text)
		text = text[size:]
		b.WriteRune(r)
		if b.Len() >= target || strings.ContainsRune("。！？.!?\n", r) {
			emit(b.String())
			b.Reset()
		}
	}
	if b.Len() > 0 {
		emit(b.String())
	}
}

func decodeLimited(r *http.Request, out any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	return dec.Decode(out)
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func safeError(err error) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(strings.TrimSpace(err.Error()), "\n", " ")
}
