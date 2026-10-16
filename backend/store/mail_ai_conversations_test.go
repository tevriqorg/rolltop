package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
)

func TestMailAIConversationPersistsAcrossReopenAndIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "rolltop.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser(ctx, "mail-ai-owner@example.test", "Owner", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "mail-ai-other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	thread, err := st.CurrentMailAIThreadForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if thread.ID == "" || len(thread.Messages) != 0 {
		t.Fatalf("new thread=%+v", thread)
	}
	messages := []MailAIMessageSnapshot{
		{ID: "u1", Role: "user", Payload: json.RawMessage(`{"id":"u1","role":"user","parts":[{"type":"text","text":"hello"}]}`)},
		{ID: "a1", Role: "assistant", Payload: json.RawMessage(`{"id":"a1","role":"assistant","parts":[{"type":"text","text":"world"},{"type":"tool-search_mail","toolCallId":"t1","state":"output-available","input":{"q":"x"},"output":[1]}]}`)},
	}
	if err := st.SaveMailAIThreadMessagesForUser(ctx, owner.ID, thread.ID, messages); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveMailAIThreadMessagesForUser(ctx, other.ID, thread.ID, messages); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant save err=%v, want not found", err)
	}
	otherThread, err := st.CurrentMailAIThreadForUser(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if otherThread.ID == thread.ID {
		t.Fatal("tenants unexpectedly share a Mail AI thread")
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}

	st, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	restored, err := st.CurrentMailAIThreadForUser(ctx, owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != thread.ID || len(restored.Messages) != 2 {
		t.Fatalf("restored thread id=%q messages=%d, want id=%q messages=2", restored.ID, len(restored.Messages), thread.ID)
	}
	if string(restored.Messages[0]) != string(messages[0].Payload) || string(restored.Messages[1]) != string(messages[1].Payload) {
		t.Fatalf("restored messages=%s / %s", restored.Messages[0], restored.Messages[1])
	}
}

func TestMailAIConversationRejectsDuplicateMessageIDs(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, _ := st.CreateUser(ctx, "mail-ai-duplicate@example.test", "Owner", "hash", false)
	thread, err := st.CurrentMailAIThreadForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	messages := []MailAIMessageSnapshot{
		{ID: "same", Role: "user", Payload: json.RawMessage(`{"id":"same","role":"user","parts":[]}`)},
		{ID: "same", Role: "assistant", Payload: json.RawMessage(`{"id":"same","role":"assistant","parts":[]}`)},
	}
	if err := st.SaveMailAIThreadMessagesForUser(ctx, user.ID, thread.ID, messages); err == nil {
		t.Fatal("duplicate message ids were accepted")
	}
}

func TestMailAIConversationHistorySeparatesScopesAndRestoresByID(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, "mail-ai-history@example.test", "History", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	global, err := st.CurrentMailAIThreadForUser(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	message, err := st.CreateMailAIThreadForUser(ctx, user.ID, MailAIThreadScopeSpec{
		Kind: MailAIThreadScopeMessage, Key: "42", Context: json.RawMessage(`{"scope":"message","message_id":42}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	mailThread, err := st.CreateMailAIThreadForUser(ctx, user.ID, MailAIThreadScopeSpec{
		Kind: MailAIThreadScopeMailThread, Key: `["personal","thread-7"]`, Context: json.RawMessage(`{"scope":"mail_thread","message_id":43}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	messages := []MailAIMessageSnapshot{{
		ID: "u-history", Role: "user", Payload: json.RawMessage(`{"id":"u-history","role":"user","parts":[{"type":"text","text":"remember this scoped conversation"}]}`),
	}}
	if err := st.SaveMailAIThreadMessagesForUser(ctx, user.ID, message.ID, messages); err != nil {
		t.Fatal(err)
	}
	restored, err := st.MailAIThreadForUser(ctx, user.ID, message.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Scope != MailAIThreadScopeMessage || restored.Preview != "remember this scoped conversation" || restored.MessageCount != 1 {
		t.Fatalf("restored=%+v", restored)
	}
	history, err := st.ListMailAIThreadsForUser(ctx, user.ID, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history count=%d, want 3", len(history))
	}
	seen := map[string]string{}
	for _, item := range history {
		seen[item.ID] = item.Scope
	}
	if seen[global.ID] != MailAIThreadScopeGlobal || seen[message.ID] != MailAIThreadScopeMessage || seen[mailThread.ID] != MailAIThreadScopeMailThread {
		t.Fatalf("history scopes=%v", seen)
	}
	currentMessage, err := st.CurrentMailAIThreadForScopeForUser(ctx, user.ID, MailAIThreadScopeSpec{
		Kind: MailAIThreadScopeMessage, Key: "42", Context: json.RawMessage(`{"scope":"message","message_id":42}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if currentMessage.ID != message.ID {
		t.Fatalf("current message thread=%q, want %q", currentMessage.ID, message.ID)
	}
}
