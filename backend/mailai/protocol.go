package mailai

import "encoding/json"

// CurrentMailContext is resolved by Mail Hub from the signed-in user's local
// mirror. Browsers provide only a message ID; they never author this context.
type CurrentMailContext struct {
	Provider  string `json:"provider"`
	Account   string `json:"account"`
	MessageID int64  `json:"message_id"`
	NativeID  string `json:"native_id"`
	ThreadID  string `json:"thread_id"`
	Subject   string `json:"subject"`
	From      string `json:"from"`
	To        string `json:"to"`
	CC        string `json:"cc"`
	Date      string `json:"date"`
	Body      string `json:"body"`
}

type ChatRequest struct {
	UserID  int64              `json:"user_id"`
	Prompt  string             `json:"prompt"`
	Context CurrentMailContext `json:"context"`
}

type ConfirmRequest struct {
	UserID      int64  `json:"user_id"`
	ActionToken string `json:"action_token"`
}

type ToolRequest struct {
	UserID    int64           `json:"user_id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Confirmed bool            `json:"confirmed,omitempty"`
}

type ToolResponse struct {
	Name   string `json:"name"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

type Event struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Arguments   any    `json:"arguments,omitempty"`
	Result      any    `json:"result,omitempty"`
	ActionToken string `json:"action_token,omitempty"`
}
