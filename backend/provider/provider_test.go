package provider

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime/multipart"
	"net/mail"
	"reflect"
	"strings"
	"testing"

	"rolltop/backend/imapclient"
	"rolltop/backend/store"
)

func collect(t *testing.T, r *Router, a Account, s State) ([]Message, State) {
	t.Helper()
	var messages []Message
	next, err := r.Fetch(context.Background(), a, s, func(m Message) error { messages = append(messages, m); return nil })
	if err != nil {
		t.Fatal(err)
	}
	return messages, next
}
func TestMockDeterministicFixturesAndIncrementalReplay(t *testing.T) {
	r, err := NewRouter(MockProvider{})
	if err != nil {
		t.Fatal(err)
	}
	a := MockAccounts(1)[0]
	s := State{Account: a}
	messages, next := collect(t, r, a, s)
	again, _ := collect(t, r, a, s)
	if !reflect.DeepEqual(messages, again) || len(messages) != 5 {
		t.Fatal("fixtures must be fixed")
	}
	if strings.Contains(next.Cursor, "uid") {
		t.Fatal("mock cursor must remain native")
	}
	empty, end := collect(t, r, a, next)
	if len(empty) != 0 || end != next {
		t.Fatal("completed checkpoint replayed")
	}
	if messages[0].ID != messages[3].ID || !bytes.Equal(messages[0].Raw, messages[3].Raw) || reflect.DeepEqual(messages[0].Folders, messages[3].Folders) {
		t.Fatal("missing duplicate observation")
	}
	if messages[0].ThreadID != messages[1].ThreadID || !messages[0].Starred || messages[0].Read || !messages[1].Read {
		t.Fatal("thread or read/star fixture missing")
	}
	if !bytes.Contains(messages[0].Raw, []byte("你好")) || !bytes.Contains(messages[1].Raw, []byte("確認")) {
		t.Fatal("CJK content missing")
	}
	if !messages[4].Oversized || messages[4].Size <= imapclient.DefaultMaxRawMessageBytes || len(messages[4].Raw) > 1024 {
		t.Fatal("oversized must be bounded header stub")
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(messages[2].Raw))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(parsed.Body, "mock-boundary")
	if _, err = reader.NextPart(); err != nil {
		t.Fatal(err)
	}
	part, err := reader.NextPart()
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(part)
	if err != nil || part.FileName() != "notes.txt" || !bytes.Contains(body, []byte("固定附件")) {
		t.Fatal("attachment MIME fixture missing")
	}
	work := MockAccounts(1)[1]
	workMessages, _ := collect(t, r, work, State{Account: work})
	if len(workMessages) != 2 || !bytes.Equal(workMessages[0].Raw, messages[0].Raw) {
		t.Fatal("cross-account duplicate missing")
	}
	// Callers can mutate observations without altering subsequent runs.
	messages[0].Raw[0] = 'X'
	messages[0].Labels[0] = "changed"
	clean, _ := collect(t, r, a, s)
	if !reflect.DeepEqual(clean, again) {
		t.Fatal("fixture mutation leaked")
	}
	filtered, filteredState := collect(t, r, a, State{Account: a, Collection: "Documents"})
	if len(filtered) != 1 || filtered[0].ID != "attachment" {
		t.Fatal("collection routing failed")
	}
	empty, _ = collect(t, r, a, filteredState)
	if len(empty) != 0 {
		t.Fatal("collection checkpoint replayed")
	}
}

func TestScopeErrorsAndFailurePreserveCheckpoint(t *testing.T) {
	r, _ := NewRouter(MockProvider{})
	a := MockAccounts(1)[0]
	s := State{Account: a}
	sentinel := errors.New("handler failed")
	next, err := r.Fetch(context.Background(), a, s, func(Message) error { return sentinel })
	if !errors.Is(err, sentinel) || next != s {
		t.Fatal("failed callback advanced cursor")
	}
	for _, bad := range []Account{{2, a.ID, Mock}, {1, "work", Mock}, {1, a.ID, IMAP}} {
		called := false
		next, err = r.Fetch(context.Background(), bad, s, func(Message) error { called = true; return nil })
		if err == nil || called || next != s {
			t.Fatal("cross-scope state accepted")
		}
	}
	for _, cursor := range []string{"history-token", mockToken("work", 2)} {
		bad := s
		bad.Cursor = cursor
		if _, err = r.Fetch(context.Background(), a, bad, func(Message) error { return nil }); err == nil {
			t.Fatal("foreign cursor accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = r.Fetch(ctx, a, s, func(Message) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	next, err = r.Fetch(ctx, a, s, func(Message) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) || next != s {
		t.Fatal("cancellation advanced cursor")
	}
	unsupported := Account{1, "example", "future"}
	if _, err = r.Fetch(context.Background(), unsupported, State{Account: unsupported, Cursor: "opaque delta token"}, func(Message) error { return nil }); err == nil {
		t.Fatal("unsupported provider fell back")
	}
	if _, err = NewRouter(MockProvider{}, MockProvider{}); err == nil {
		t.Fatal("duplicate registration")
	}
}

func TestOpaqueStatePassesThroughRouter(t *testing.T) {
	a := Account{1, "native", "test-native"}
	s := State{Account: a, Cursor: "https://example.invalid/delta?$token=opaque"}
	r, _ := NewRouter(opaqueProvider{})
	_, next := collect(t, r, a, s)
	if next != s {
		t.Fatal("router interpreted native cursor")
	}
}

type opaqueProvider struct{}

func (opaqueProvider) Kind() Kind { return "test-native" }
func (opaqueProvider) Fetch(_ context.Context, _ Account, s State, _ func(Message) error) (State, error) {
	return s, nil
}

func TestIMAPWrapperPreservesEntireFetcherMethodSet(t *testing.T) {
	original := reflect.TypeOf(&imapclient.Fetcher{})
	wrapper := reflect.TypeOf(&IMAPProvider{})
	for i := 0; i < original.NumMethod(); i++ {
		method := original.Method(i)
		promoted, ok := wrapper.MethodByName(method.Name)
		if !ok || method.Type.NumIn() != promoted.Type.NumIn() || method.Type.NumOut() != promoted.Type.NumOut() {
			t.Fatalf("lost IMAP method %s", method.Name)
		}
		for j := 1; j < method.Type.NumIn(); j++ {
			if method.Type.In(j) != promoted.Type.In(j) {
				t.Fatalf("changed %s", method.Name)
			}
		}
		for j := 0; j < method.Type.NumOut(); j++ {
			if method.Type.Out(j) != promoted.Type.Out(j) {
				t.Fatalf("changed %s", method.Name)
			}
		}
	}
	p := &IMAPProvider{Fetcher: &imapclient.Fetcher{}}
	a := Account{1, "7", IMAP}
	s := State{Account: a, Collection: "INBOX"}
	// Reject mismatches and malformed state before any decrypt or dial.
	for _, source := range []store.MailAccount{{ID: 7, UserID: 2}, {ID: 8, UserID: 1}} {
		if _, err := p.FetchIMAP(context.Background(), a, source, s, func(Message) error { return nil }); err == nil {
			t.Fatal("source scope mismatch accepted")
		}
	}
	s.Cursor = "opaque-not-imap"
	if _, err := p.FetchIMAP(context.Background(), a, store.MailAccount{ID: 7, UserID: 1}, s, func(Message) error { return nil }); err == nil {
		t.Fatal("malformed cursor accepted")
	}
	r, _ := NewRouter(p.Bind(store.MailAccount{ID: 7, UserID: 2}))
	if _, err := r.Fetch(context.Background(), a, s, func(Message) error { return nil }); err == nil {
		t.Fatal("bound route accepted another tenant")
	}
}
