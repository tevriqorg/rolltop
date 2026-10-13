package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"rolltop/backend/store"
	"rolltop/backend/syncer"
)

type adapterReader struct {
	after, generation uint32
	mailbox           string
	probes            int
	fail              error
}

func (r *adapterReader) MailboxStatus(context.Context, store.MailAccount, string) (syncer.MailboxStatus, error) {
	r.probes++
	return syncer.MailboxStatus{UIDValidity: 42}, nil
}
func (r *adapterReader) FetchMailboxWithUIDValidity(_ context.Context, _ store.MailAccount, mailbox string, after, generation uint32, handle func(syncer.FetchedMessage) error) error {
	r.after, r.generation, r.mailbox = after, generation, mailbox
	if err := handle(syncer.FetchedMessage{Mailbox: mailbox, UID: 9, UIDValidity: 42, Raw: []byte("raw"), Flags: []string{"\\Seen", "\\Flagged"}, Size: 3}); err != nil {
		return err
	}
	return r.fail
}
func TestIMAPAdapterGenerationCheckpointAndMapping(t *testing.T) {
	a := Account{1, "7", IMAP}
	state := State{Account: a, Collection: "INBOX"}
	r := &adapterReader{}
	var message Message
	next, err := fetchIMAP(context.Background(), r, store.MailAccount{}, state, func(m Message) error { message = m; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if r.after != 0 || r.generation != 42 || r.mailbox != "INBOX" || r.probes != 1 {
		t.Fatal("wrong initial IMAP request")
	}
	if message.ID != "INBOX:42:9" || !message.Read || !message.Starred || string(message.Raw) != "raw" || message.Size != 3 {
		t.Fatal("mapping lost IMAP metadata")
	}
	var cursor imapCursor
	if err = json.Unmarshal([]byte(next.Cursor), &cursor); err != nil || cursor.UID != 9 || cursor.UIDValidity != 42 {
		t.Fatal("checkpoint lost generation")
	}
	_, err = fetchIMAP(context.Background(), r, store.MailAccount{}, next, func(Message) error { return nil })
	if err != nil || r.after != 9 || r.generation != 42 || r.probes != 1 {
		t.Fatal("incremental request changed")
	}
	sentinel := errors.New("fetch failed")
	r.fail = sentinel
	failed, err := fetchIMAP(context.Background(), r, store.MailAccount{}, next, func(Message) error { return nil })
	if !errors.Is(err, sentinel) || failed != next {
		t.Fatal("fetch failure advanced checkpoint")
	}
	r.fail = nil
	failed, err = fetchIMAP(context.Background(), r, store.MailAccount{}, state, func(Message) error { return sentinel })
	if !errors.Is(err, sentinel) || failed != state {
		t.Fatal("callback failure advanced checkpoint")
	}
}
