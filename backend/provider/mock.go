package provider

import (
	"context"
	"errors"
	"fmt"
)

// MockProvider has no transport, credentials, clock, filesystem, or network
// dependencies. All fixture bytes are synthesized from fixed public-safe data.
type MockProvider struct{}

var _ MailProvider = MockProvider{}

func (MockProvider) Kind() Kind { return Mock }

// MockAccounts returns independent source identities for the given tenant.
func MockAccounts(userID int64) []Account {
	return []Account{{userID, "personal", Mock}, {userID, "work", Mock}}
}

func (MockProvider) Fetch(ctx context.Context, account Account, state State, handle func(Message) error) (State, error) {
	if err := validate(ctx, account, state, handle); err != nil {
		return state, err
	}
	if account.Provider != Mock || (account.ID != "personal" && account.ID != "work") {
		return state, errors.New("unknown mock account")
	}
	fixtures := mockFixtures(account.ID)
	start := 0
	if state.Cursor != "" {
		found := false
		for i := range fixtures {
			if state.Cursor == mockToken(account.ID, i+1) {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return state, errors.New("invalid or expired mock cursor")
		}
	}
	next := state
	for i := start; i < len(fixtures); i++ {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		m := fixtures[i]
		if state.Collection != "" && !contains(m.Folders, state.Collection) && !contains(m.Labels, state.Collection) {
			continue
		}
		if err := handle(m); err != nil {
			return state, err
		}
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	next.Cursor = mockToken(account.ID, len(fixtures))
	return next, nil
}
func mockToken(account string, offset int) string {
	return fmt.Sprintf("mock-v1:%s:observation-%d", account, offset)
}
func contains(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}

func mockFixtures(account string) []Message {
	raw := func(id, subject, extra, body string) []byte {
		return []byte("From: sender@example.invalid\r\nTo: recipient@example.invalid\r\nDate: Mon, 01 Jun 2026 09:00:00 +0000\r\nMessage-ID: <" + id + "@example.invalid>\r\nSubject: " + subject + "\r\nMIME-Version: 1.0\r\n" + extra + "\r\n" + body)
	}
	text := "Content-Type: text/plain; charset=utf-8\r\n"
	chinese := raw("planning", "项目计划", text, "你好，附件是项目计划。\r\n")
	messages := []Message{
		{ID: "planning", ThreadID: "project", MessageID: "<planning@example.invalid>", Folders: []string{"INBOX"}, Labels: []string{"项目", "important"}, Starred: true, Raw: chinese},
		{ID: "reply", ThreadID: "project", MessageID: "<reply@example.invalid>", Folders: []string{"INBOX"}, Labels: []string{"项目"}, Read: true, Raw: raw("reply", "Re: 项目计划", "In-Reply-To: <planning@example.invalid>\r\nReferences: <planning@example.invalid>\r\n"+text, "確認しました。来週の会議で相談しましょう。\r\n")},
		{ID: "attachment", ThreadID: "files", MessageID: "<attachment@example.invalid>", Folders: []string{"Documents"}, Read: true, Starred: true, Raw: raw("attachment", "Meeting notes", "Content-Type: multipart/mixed; boundary=mock-boundary\r\n", "--mock-boundary\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nMeeting notes attached.\r\n--mock-boundary\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Disposition: attachment; filename=notes.txt\r\n\r\n固定附件 / 固定添付ファイル\r\n--mock-boundary--\r\n")},
		// Same canonical ID/raw observed under another label, not a new message.
		{ID: "planning", ThreadID: "project", MessageID: "<planning@example.invalid>", Folders: []string{"Projects"}, Labels: []string{"项目"}, Starred: true, Raw: append([]byte(nil), chinese...)},
		{ID: "oversized", ThreadID: "large", MessageID: "<oversized@example.invalid>", Folders: []string{"INBOX"}, Size: 51 * 1024 * 1024, Oversized: true, Raw: raw("oversized", "Large message", text, "")},
	}
	for i := range messages {
		if messages[i].Size == 0 {
			messages[i].Size = int64(len(messages[i].Raw))
		}
	}
	if account == "work" {
		return []Message{messages[0], messages[2]}
	} // Same raw across accounts preserves separate provenance.
	return messages
}
