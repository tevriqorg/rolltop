package provider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

type RemoteMockMailbox struct {
	Account    Account
	BaseURL    string
	HTTPClient *http.Client
}

func (p RemoteMockMailbox) Kind() Kind { return Mock }
func (p RemoteMockMailbox) client() *http.Client {
	if p.HTTPClient != nil {
		return p.HTTPClient
	}
	return &http.Client{Timeout: 15 * time.Second}
}

type remoteList struct {
	Messages []struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	} `json:"messages"`
	NextPageToken string `json:"nextPageToken"`
}
type remoteMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	HistoryID    string   `json:"historyId"`
	InternalDate string   `json:"internalDate"`
	Raw          string   `json:"raw"`
	LabelIDs     []string `json:"labelIds"`
	SizeEstimate int64    `json:"sizeEstimate"`
}
type remoteHistory struct {
	History []struct {
		ID            string `json:"id"`
		MessagesAdded []struct {
			Message struct {
				ID       string `json:"id"`
				ThreadID string `json:"threadId"`
			} `json:"message"`
		} `json:"messagesAdded"`
		LabelsAdded []struct {
			Message struct {
				ID       string `json:"id"`
				ThreadID string `json:"threadId"`
			} `json:"message"`
		} `json:"labelsAdded"`
	} `json:"history"`
	HistoryID string `json:"historyId"`
}

type remoteProfile struct {
	HistoryID string `json:"historyId"`
}

func (p RemoteMockMailbox) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	res, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("mock source GET %s: HTTP %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (p RemoteMockMailbox) postJSON(ctx context.Context, path string, in, out any) error {
	body, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("mock source POST %s: HTTP %d", path, res.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(res.Body).Decode(out)
}

func (p RemoteMockMailbox) profile(ctx context.Context) (remoteProfile, error) {
	var v remoteProfile
	err := p.getJSON(ctx, "/gmail/v1/users/me/profile", &v)
	return v, err
}
func (p RemoteMockMailbox) ids(ctx context.Context, cursor string) ([]string, string, error) {
	seen := map[string]bool{}
	ids := []string{}
	if cursor != "" {
		var h remoteHistory
		if err := p.getJSON(ctx, "/gmail/v1/users/me/history?startHistoryId="+url.QueryEscape(cursor), &h); err != nil {
			return nil, cursor, err
		}
		for _, ev := range h.History {
			for _, x := range ev.MessagesAdded {
				if !seen[x.Message.ID] {
					seen[x.Message.ID] = true
					ids = append(ids, x.Message.ID)
				}
			}
			for _, x := range ev.LabelsAdded {
				if !seen[x.Message.ID] {
					seen[x.Message.ID] = true
					ids = append(ids, x.Message.ID)
				}
			}
		}
		return ids, h.HistoryID, nil
	}
	token := ""
	for {
		var page remoteList
		path := "/gmail/v1/users/me/messages?maxResults=100"
		if token != "" {
			path += "&pageToken=" + url.QueryEscape(token)
		}
		if err := p.getJSON(ctx, path, &page); err != nil {
			return nil, "", err
		}
		for _, x := range page.Messages {
			ids = append(ids, x.ID)
		}
		if page.NextPageToken == "" {
			break
		}
		token = page.NextPageToken
	}
	prof, err := p.profile(ctx)
	return ids, prof.HistoryID, err
}

func (p RemoteMockMailbox) message(ctx context.Context, id string) (Message, error) {
	var rm remoteMessage
	if err := p.getJSON(ctx, "/gmail/v1/users/me/messages/"+url.PathEscape(id)+"?format=raw", &rm); err != nil {
		return Message{}, err
	}
	raw, err := base64.RawURLEncoding.DecodeString(rm.Raw)
	if err != nil {
		return Message{}, err
	}
	parsed, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return Message{}, err
	}
	folders := []string{}
	labels := []string{}
	read := true
	starred := false
	for _, l := range rm.LabelIDs {
		switch l {
		case "INBOX":
			folders = append(folders, "INBOX")
		case "TRASH":
			folders = append(folders, "Trash")
		case "UNREAD":
			read = false
		case "STARRED":
			starred = true
		default:
			labels = append(labels, l)
		}
	}
	return Message{ID: rm.ID, ThreadID: rm.ThreadID, MessageID: parsed.Header.Get("Message-ID"), Folders: folders, Labels: labels, Read: read, Starred: starred, Raw: raw, Size: rm.SizeEstimate}, nil
}
func (p RemoteMockMailbox) Fetch(ctx context.Context, a Account, state State, handle func(Message) error) (State, error) {
	if err := validate(ctx, a, state, handle); err != nil {
		return state, err
	}
	if a != p.Account || strings.TrimSpace(p.BaseURL) == "" {
		return state, errors.New("remote mock source scope mismatch")
	}
	ids, next, err := p.ids(ctx, state.Cursor)
	if err != nil {
		return state, err
	}
	for _, id := range ids {
		m, err := p.message(ctx, id)
		if err != nil {
			return state, err
		}
		if err = handle(m); err != nil {
			return state, err
		}
	}
	state.Cursor = next
	return state, nil
}

func (p RemoteMockMailbox) Snapshot(ctx context.Context) ([]Message, error) {
	out := []Message{}
	_, err := p.Fetch(ctx, p.Account, State{Account: p.Account}, func(m Message) error { out = append(out, m); return nil })
	return out, err
}

func (p RemoteMockMailbox) Census(ctx context.Context, a Account) (Census, error) {
	if a != p.Account {
		return Census{}, errors.New("census scope mismatch")
	}
	msgs, err := p.Snapshot(ctx)
	if err != nil {
		return Census{}, err
	}
	c := Census{Account: a, MessageCount: len(msgs), Method: "remote Gmail-like messages.list + messages.get(sizeEstimate)", Exact: true}
	for _, m := range msgs {
		c.EstimatedRawBytes += m.Size
	}
	c.ExpectedEvidenceBytes = c.EstimatedRawBytes
	c.RecommendedFreeDisk = c.EstimatedRawBytes + (c.EstimatedRawBytes+1)/2
	return c, nil
}

func (p RemoteMockMailbox) Mutate(ctx context.Context, change Mutation) error {
	if strings.TrimSpace(change.MessageID) == "" {
		return errors.New("message id required")
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
		add = append(add, "INBOX")
		remove = append(remove, "TRASH")
	case "label":
		label := strings.TrimSpace(change.Label)
		if label == "" {
			return errors.New("label required")
		}
		if change.Value {
			add = append(add, label)
		} else {
			remove = append(remove, label)
		}
	default:
		return errors.New("unsupported mock mutation")
	}
	return p.postJSON(ctx, "/gmail/v1/users/me/messages/"+url.PathEscape(change.MessageID)+"/modify", map[string]any{"addLabelIds": add, "removeLabelIds": remove}, nil)
}
