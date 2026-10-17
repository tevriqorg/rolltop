package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"sort"
	"strings"

	"rolltop/backend/provider"
)

var ErrHistoryExpired = errors.New("gmail history cursor expired; full recovery required")

type Provider struct {
	Client  Client
	Account provider.Account
}

func (Provider) Kind() provider.Kind { return provider.Gmail }

type apiHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}
type apiMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	HistoryID    string   `json:"historyId"`
	LabelIDs     []string `json:"labelIds"`
	SizeEstimate int64    `json:"sizeEstimate"`
	Raw          string   `json:"raw"`
	Payload      struct {
		Headers []apiHeader `json:"headers"`
	} `json:"payload"`
}
type apiList struct {
	Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`
	NextPageToken string `json:"nextPageToken"`
}
type apiProfile struct {
	EmailAddress string `json:"emailAddress"`
	HistoryID    string `json:"historyId"`
}
type apiLabel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}
type apiHistoryMessage struct {
	Message apiMessage `json:"message"`
}
type apiHistory struct {
	ID              string              `json:"id"`
	MessagesAdded   []apiHistoryMessage `json:"messagesAdded"`
	MessagesDeleted []apiHistoryMessage `json:"messagesDeleted"`
	LabelsAdded     []apiHistoryMessage `json:"labelsAdded"`
	LabelsRemoved   []apiHistoryMessage `json:"labelsRemoved"`
}
type apiHistoryPage struct {
	History       []apiHistory `json:"history"`
	HistoryID     string       `json:"historyId"`
	NextPageToken string       `json:"nextPageToken"`
}

func (p Provider) validate(a provider.Account, state provider.State, handle func(provider.Message) error) error {
	if a.UserID <= 0 || a.ID == "" || a.Provider != provider.Gmail {
		return errors.New("gmail provider requires a tenant gmail account")
	}
	if p.Account != a || state.Account != a {
		return errors.New("gmail provider scope mismatch")
	}
	if state.Collection != "" {
		return errors.New("gmail provider uses account-wide history state")
	}
	if handle == nil {
		return errors.New("gmail provider requires a message handler")
	}
	return nil
}

func (p Provider) Fetch(ctx context.Context, a provider.Account, state provider.State, handle func(provider.Message) error) (provider.State, error) {
	if err := p.validate(a, state, handle); err != nil {
		return state, err
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	labels, err := p.Client.labelMap(ctx)
	if err != nil {
		return state, err
	}
	if state.Cursor == "" {
		return p.fullFetch(ctx, state, labels, handle)
	}
	return p.historyFetch(ctx, state, labels, handle)
}

func (p Provider) fullFetch(ctx context.Context, state provider.State, labels map[string]string, handle func(provider.Message) error) (provider.State, error) {
	var profile apiProfile
	if err := p.Client.getJSON(ctx, "/users/me/profile?fields=emailAddress,historyId", &profile); err != nil {
		return state, err
	}
	if strings.TrimSpace(profile.HistoryID) == "" {
		return state, errors.New("gmail profile did not include historyId")
	}
	token := ""
	for {
		q := url.Values{"maxResults": {"500"}, "includeSpamTrash": {"true"}, "fields": {"messages/id,messages/threadId,nextPageToken"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page apiList
		if err := p.Client.getJSON(ctx, "/users/me/messages?"+q.Encode(), &page); err != nil {
			return state, err
		}
		for _, ref := range page.Messages {
			msg, err := p.Client.message(ctx, ref.ID, labels)
			if err != nil {
				return state, err
			}
			if err := handle(msg); err != nil {
				return state, err
			}
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	state.Cursor = profile.HistoryID
	return state, nil
}

func (p Provider) historyFetch(ctx context.Context, state provider.State, labels map[string]string, handle func(provider.Message) error) (provider.State, error) {
	token := ""
	affected := map[string]bool{}
	deleted := map[string]apiMessage{}
	nextHistory := state.Cursor
	for {
		q := url.Values{"startHistoryId": {state.Cursor}, "maxResults": {"500"}, "historyTypes": {"messageAdded,labelAdded,labelRemoved,messageDeleted"}, "fields": {"history(id,messagesAdded/message(id,threadId),messagesDeleted/message(id,threadId),labelsAdded/message(id,threadId),labelsRemoved/message(id,threadId)),historyId,nextPageToken"}}
		if token != "" {
			q.Set("pageToken", token)
		}
		var page apiHistoryPage
		err := p.Client.getJSON(ctx, "/users/me/history?"+q.Encode(), &page)
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			return state, ErrHistoryExpired
		}
		if err != nil {
			return state, err
		}
		if strings.TrimSpace(page.HistoryID) != "" {
			nextHistory = page.HistoryID
		}
		for _, ev := range page.History {
			for _, x := range ev.MessagesAdded {
				affected[x.Message.ID] = true
				delete(deleted, x.Message.ID)
			}
			for _, x := range ev.LabelsAdded {
				affected[x.Message.ID] = true
				delete(deleted, x.Message.ID)
			}
			for _, x := range ev.LabelsRemoved {
				affected[x.Message.ID] = true
				delete(deleted, x.Message.ID)
			}
			for _, x := range ev.MessagesDeleted {
				delete(affected, x.Message.ID)
				deleted[x.Message.ID] = x.Message
			}
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	ids := make([]string, 0, len(affected))
	for id := range affected {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		msg, err := p.Client.message(ctx, id, labels)
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			deleted[id] = apiMessage{ID: id}
			continue
		}
		if err != nil {
			return state, err
		}
		if err := handle(msg); err != nil {
			return state, err
		}
	}
	delIDs := make([]string, 0, len(deleted))
	for id := range deleted {
		if id != "" {
			delIDs = append(delIDs, id)
		}
	}
	sort.Strings(delIDs)
	for _, id := range delIDs {
		if err := handle(provider.Message{ID: id, ThreadID: deleted[id].ThreadID, Deleted: true}); err != nil {
			return state, err
		}
	}
	state.Cursor = nextHistory
	return state, nil
}

func (c Client) labelMap(ctx context.Context) (map[string]string, error) {
	var out struct {
		Labels []apiLabel `json:"labels"`
	}
	if err := c.getJSON(ctx, "/users/me/labels?fields=labels(id,name,type)", &out); err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, label := range out.Labels {
		id := strings.TrimSpace(label.ID)
		name := strings.TrimSpace(label.Name)
		if id != "" {
			if name == "" {
				name = id
			}
			m[id] = name
		}
	}
	return m, nil
}
func decodeRaw(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("gmail message did not include raw MIME")
	}
	if b, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return b, nil
	}
	return base64.URLEncoding.DecodeString(value)
}
func (c Client) message(ctx context.Context, id string, labels map[string]string) (provider.Message, error) {
	q := url.Values{"format": {"metadata"}, "metadataHeaders": {"Message-ID"}, "fields": {"id,threadId,historyId,labelIds,sizeEstimate,payload/headers,raw"}}
	var meta apiMessage
	if err := c.getJSON(ctx, "/users/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), &meta); err != nil {
		return provider.Message{}, err
	}
	if meta.ID == "" {
		meta.ID = id
	}
	var raw []byte
	var err error
	oversized := c.MaxRawMessageBytes > 0 && meta.SizeEstimate > c.MaxRawMessageBytes
	if oversized {
		raw = oversizedStub(meta)
	} else {
		q = url.Values{"format": {"raw"}, "fields": {"id,threadId,historyId,labelIds,sizeEstimate,raw"}}
		var full apiMessage
		if err = c.getJSON(ctx, "/users/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), &full); err != nil {
			return provider.Message{}, err
		}
		if full.ID != "" {
			meta = full
		}
		raw, err = decodeRaw(full.Raw)
		if err != nil {
			return provider.Message{}, err
		}
	}
	msgID := ""
	if parsed, e := mail.ReadMessage(bytes.NewReader(raw)); e == nil {
		msgID = parsed.Header.Get("Message-ID")
	}
	folders, userLabels, read, starred := mapLabels(meta.LabelIDs, labels)
	return provider.Message{ID: meta.ID, ThreadID: meta.ThreadID, MessageID: msgID, Folders: folders, Labels: userLabels, Read: read, Starred: starred, Raw: raw, Size: meta.SizeEstimate, Oversized: oversized}, nil
}
func oversizedStub(meta apiMessage) []byte {
	var b strings.Builder
	foundMessageID := false
	for _, h := range meta.Payload.Headers {
		name := strings.TrimSpace(h.Name)
		value := strings.TrimSpace(h.Value)
		if name == "" || value == "" {
			continue
		}
		if strings.EqualFold(name, "Message-ID") {
			foundMessageID = true
		}
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(value)
		b.WriteString("\r\n")
	}
	if !foundMessageID {
		b.WriteString("Message-ID: <gmail-")
		b.WriteString(meta.ID)
		b.WriteString("@oversized.rolltop.invalid>\r\n")
	}
	b.WriteString("X-Rolltop-Oversized: true\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n[message body omitted by configured raw-size limit]\r\n")
	return []byte(b.String())
}
func mapLabels(ids []string, names map[string]string) (folders, labels []string, read, starred bool) {
	read = true
	for _, id := range ids {
		switch id {
		case "INBOX":
			folders = appendUnique(folders, "INBOX")
		case "TRASH":
			folders = appendUnique(folders, "Trash")
		case "SPAM":
			folders = appendUnique(folders, "Spam")
		case "SENT":
			folders = appendUnique(folders, "Sent")
		case "DRAFT":
			folders = appendUnique(folders, "Drafts")
		case "UNREAD":
			read = false
		case "STARRED":
			starred = true
		default:
			if strings.HasPrefix(id, "CATEGORY_") || id == "IMPORTANT" {
				labels = appendUnique(labels, id)
			} else if name := strings.TrimSpace(names[id]); name != "" {
				labels = appendUnique(labels, name)
			} else if id != "" {
				labels = appendUnique(labels, id)
			}
		}
	}
	return
}
func appendUnique(in []string, v string) []string {
	for _, x := range in {
		if x == v {
			return in
		}
	}
	return append(in, v)
}

func (c Client) resolveLabelID(ctx context.Context, label string) (string, error) {
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("gmail label is required")
	}
	m, err := c.labelMap(ctx)
	if err != nil {
		return "", err
	}
	for id, name := range m {
		if id == label || strings.EqualFold(name, label) {
			return id, nil
		}
	}
	return "", fmt.Errorf("gmail label %q does not exist", label)
}
func (c Client) Mutate(ctx context.Context, change provider.Mutation) error {
	id := strings.TrimSpace(change.MessageID)
	if id == "" {
		return errors.New("gmail mutation requires message id")
	}
	add, remove := []string{}, []string{}
	switch change.Action {
	case "read":
		if change.Value {
			remove = append(remove, "UNREAD")
		} else {
			add = append(add, "UNREAD")
		}
	case "star":
		if change.Value {
			add = append(add, "STARRED")
		} else {
			remove = append(remove, "STARRED")
		}
	case "archive":
		remove = append(remove, "INBOX")
	case "trash":
		add = append(add, "TRASH")
		remove = append(remove, "INBOX")
	case "restore":
		remove = append(remove, "TRASH")
		add = append(add, "INBOX")
	case "label":
		labelID, err := c.resolveLabelID(ctx, change.Label)
		if err != nil {
			return err
		}
		if change.Value {
			add = append(add, labelID)
		} else {
			remove = append(remove, labelID)
		}
	default:
		return fmt.Errorf("unsupported gmail mutation %q", change.Action)
	}
	return c.postJSON(ctx, "/users/me/messages/"+url.PathEscape(id)+"/modify", map[string]any{"addLabelIds": add, "removeLabelIds": remove}, nil)
}

type SentMessage struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId"`
	LabelIDs []string `json:"labelIds"`
}

func (c Client) SendRaw(ctx context.Context, raw []byte) (SentMessage, error) {
	if len(raw) == 0 {
		return SentMessage{}, errors.New("gmail send requires raw MIME")
	}
	var out SentMessage
	err := c.postJSON(ctx, "/users/me/messages/send", map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}, &out)
	if err != nil {
		return SentMessage{}, err
	}
	if strings.TrimSpace(out.ID) == "" {
		return SentMessage{}, errors.New("gmail send response missing message id")
	}
	return out, nil
}
