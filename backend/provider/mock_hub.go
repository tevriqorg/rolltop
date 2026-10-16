package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Census is metadata-only capacity planning. Evidence bytes are a budget, not
// captured data. RecommendedFreeDisk uses the issue's provisional 1.5x model.
type Census struct {
	Account               Account `json:"account"`
	MessageCount          int     `json:"message_count"`
	EstimatedRawBytes     int64   `json:"estimated_raw_bytes"`
	ExpectedEvidenceBytes int64   `json:"expected_evidence_bytes"`
	RecommendedFreeDisk   int64   `json:"recommended_free_disk"`
	Method                string  `json:"method"`
	Exact                 bool    `json:"exact"`
}
type CensusProvider interface {
	Census(context.Context, Account) (Census, error)
}

type Mutation struct {
	MessageID string `json:"message_id"`
	Action    string `json:"action"`
	Value     bool   `json:"value"`
	Label     string `json:"label"`
}

// MockMailbox is a reproducible source: fixed fixtures plus an ordered mutation
// journal. The embedding persists that journal; no credential or network exists.
type MockMailbox struct {
	Account Account
	Journal []Mutation
}

func (MockMailbox) Kind() Kind { return Mock }
func (p MockMailbox) Snapshot(ctx context.Context) ([]Message, error) {
	var messages []Message
	_, err := (MockProvider{}).Fetch(ctx, p.Account, State{Account: p.Account}, func(m Message) error {
		for i := range messages {
			if messages[i].ID == m.ID {
				messages[i].Folders = union(messages[i].Folders, m.Folders)
				messages[i].Labels = union(messages[i].Labels, m.Labels)
				return nil
			}
		}
		messages = append(messages, m)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, mutation := range p.Journal {
		found := false
		for i := range messages {
			if messages[i].ID == mutation.MessageID {
				found = true
				if err := applyMutation(&messages[i], mutation); err != nil {
					return nil, err
				}
			}
		}
		if !found {
			return nil, errors.New("unknown mock message")
		}
	}
	return messages, nil
}
func (p *MockMailbox) Mutate(ctx context.Context, mutation Mutation) error {
	messages, err := p.Snapshot(ctx)
	if err != nil {
		return err
	}
	for _, m := range messages {
		if m.ID == mutation.MessageID {
			if err := applyMutation(&m, mutation); err != nil {
				return err
			}
			p.Journal = append(p.Journal, mutation)
			return nil
		}
	}
	return errors.New("unknown mock message")
}
func applyMutation(m *Message, change Mutation) error {
	switch change.Action {
	case "read":
		m.Read = change.Value
	case "star":
		m.Starred = change.Value
	case "label":
		label := strings.TrimSpace(change.Label)
		if label == "" || len(label) > 240 {
			return errors.New("label must contain 1–240 bytes")
		}
		if change.Value {
			m.Labels = union(m.Labels, []string{label})
		} else {
			m.Labels = without(m.Labels, label)
		}
	case "archive":
		m.Folders = without(m.Folders, "INBOX")
	case "trash":
		m.Folders = []string{"Trash"}
	case "restore":
		m.Folders = union(without(m.Folders, "Trash"), []string{"INBOX"})
	default:
		return errors.New("unsupported mock mutation")
	}
	return nil
}
func union(a, b []string) []string {
	out := append([]string{}, a...)
	for _, v := range b {
		if !contains(out, v) {
			out = append(out, v)
		}
	}
	return out
}
func without(a []string, v string) []string {
	out := []string{}
	for _, x := range a {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}
func (p MockMailbox) Fetch(ctx context.Context, a Account, state State, handle func(Message) error) (State, error) {
	if err := validate(ctx, a, state, handle); err != nil {
		return state, err
	}
	if a != p.Account || state.Collection != "" {
		return state, errors.New("mock snapshot scope mismatch")
	}
	token := fmt.Sprintf("mock-hub-v1:%d", len(p.Journal))
	if state.Cursor == token {
		return state, nil
	}
	if state.Cursor != "" {
		version, err := strconv.Atoi(strings.TrimPrefix(state.Cursor, "mock-hub-v1:"))
		if err != nil || version < 0 || version > len(p.Journal) || !strings.HasPrefix(state.Cursor, "mock-hub-v1:") {
			return state, errors.New("invalid mock snapshot cursor")
		}
	}
	messages, err := p.Snapshot(ctx)
	if err != nil {
		return state, err
	}
	for _, m := range messages {
		if err := ctx.Err(); err != nil {
			return state, err
		}
		if err := handle(m); err != nil {
			return state, err
		}
	}
	if err := ctx.Err(); err != nil {
		return state, err
	}
	state.Cursor = token
	return state, nil
}
func (p MockMailbox) Census(ctx context.Context, a Account) (Census, error) {
	if a != p.Account {
		return Census{}, errors.New("census scope mismatch")
	}
	messages, err := p.Snapshot(ctx)
	if err != nil {
		return Census{}, err
	}
	c := Census{Account: a, MessageCount: len(messages), Method: "synthetic canonical message sizes", Exact: true}
	for _, m := range messages {
		c.EstimatedRawBytes += m.Size
	}
	c.ExpectedEvidenceBytes = c.EstimatedRawBytes
	c.RecommendedFreeDisk = c.EstimatedRawBytes + (c.EstimatedRawBytes+1)/2
	return c, nil
}
