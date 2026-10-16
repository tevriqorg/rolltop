package web

import (
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

func TestMailAIAttachmentsAreListedBeforeBoundedOnDemandRead(t *testing.T) {
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
	user, err := st.CreateUser(ctx, "attachment-ai@example.invalid", "Attachment AI", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, blobs: blob.New(dir), search: index, mailAISharedToken: "test-secret"}
	hub := mailhub.Service{Store: st, Blobs: s.blobs, Search: index}
	for _, account := range provider.MockAccounts(user.ID) {
		if err := hub.Sync(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := hub.Messages(ctx, user.ID, mailhub.Filter{Query: "Meeting notes"})
	if err != nil || len(messages) == 0 {
		t.Fatalf("messages=%d err=%v", len(messages), err)
	}
	messageID := messages[0].ID
	req := httptest.NewRequest("POST", "/api/mail-ai/tools", nil)

	meta, status, err := s.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "read_attachments", Arguments: json.RawMessage(`{"message_id":` + strconvInt64(messageID) + `}`)})
	if err != nil || status != 200 {
		t.Fatalf("metadata status=%d err=%v result=%+v", status, err, meta.Result)
	}
	metaMap := meta.Result.(map[string]any)
	attachments := metaMap["attachments"].([]mailAIAttachmentResult)
	if len(attachments) != 1 || attachments[0].Text != "" || attachments[0].Data != "" {
		t.Fatalf("metadata must not include attachment content: %+v", attachments)
	}
	readArgs, _ := json.Marshal(map[string]any{"message_id": messageID, "attachment_ids": []int64{attachments[0].ID}})
	read, status, err := s.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "read_attachments", Arguments: readArgs})
	if err != nil || status != 200 {
		t.Fatalf("read status=%d err=%v result=%+v", status, err, read.Result)
	}
	readItems := read.Result.(map[string]any)["attachments"].([]mailAIAttachmentResult)
	if len(readItems) != 1 || !strings.Contains(readItems[0].Text, "固定附件") {
		t.Fatalf("bounded attachment text=%+v", readItems)
	}

	tooMany, _ := json.Marshal(map[string]any{"message_id": messageID, "attachment_ids": []int64{attachments[0].ID, attachments[0].ID, attachments[0].ID, attachments[0].ID}})
	if _, status, err := s.runMailAITool(req, mailai.ToolRequest{UserID: user.ID, Name: "read_attachments", Arguments: tooMany}); status != 400 || err == nil {
		t.Fatalf("too-many attachments status=%d err=%v", status, err)
	}
}

func TestMailHubRemainsUsableWhenMailAIIsUnavailable(t *testing.T) {
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
	user, err := st.CreateUser(ctx, "degraded@example.invalid", "Degraded", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{store: st, blobs: blob.New(dir), search: index, mailAIURL: "http://127.0.0.1:1", mailAISharedToken: "test-secret", mailAIHTTPClient: &http.Client{Timeout: 100 * time.Millisecond}}
	hub := mailhub.Service{Store: st, Blobs: s.blobs, Search: index}
	for _, account := range provider.MockAccounts(user.ID) {
		if err := hub.Sync(ctx, account); err != nil {
			t.Fatal(err)
		}
	}
	messages, err := hub.Messages(ctx, user.ID, mailhub.Filter{Account: "personal"})
	if err != nil || len(messages) == 0 {
		t.Fatalf("messages=%d err=%v", len(messages), err)
	}

	mailReq := httptest.NewRequest(http.MethodGet, "/api/messages/"+strconvInt64(messages[0].ID), nil)
	mailReq = mailReq.WithContext(context.WithValue(mailReq.Context(), userContextKey, currentUser{User: user}))
	mailRec := httptest.NewRecorder()
	s.handleAPI(mailRec, mailReq)
	if mailRec.Code != http.StatusOK {
		t.Fatalf("ordinary mail failed while AI unavailable: %d %s", mailRec.Code, mailRec.Body.String())
	}

	aiReq := httptest.NewRequest(http.MethodGet, "/api/mail-ai/model-config", nil)
	aiReq = aiReq.WithContext(context.WithValue(aiReq.Context(), userContextKey, currentUser{User: user}))
	aiRec := httptest.NewRecorder()
	s.handleAPI(aiRec, aiReq)
	if aiRec.Code != http.StatusServiceUnavailable {
		t.Fatalf("AI failure state=%d %s", aiRec.Code, aiRec.Body.String())
	}
}
