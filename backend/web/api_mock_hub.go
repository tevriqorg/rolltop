package web

import (
	"context"
	"encoding/json"
	"net/http"
	"rolltop/backend/mailhub"
	"rolltop/backend/provider"
	"rolltop/backend/store"
)

func (s *Server) apiMockHub(w http.ResponseWriter, r *http.Request, path string) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	// All native provider state comes from the signed-in tenant. Browser input
	// cannot choose a user, transport, credential, or provider implementation.
	s.mockHubMu.Lock()
	defer s.mockHubMu.Unlock()
	hub := mailhub.Service{Store: s.store, Blobs: s.blobs, Search: s.search}
	if path == "mock-hub" && r.Method == http.MethodGet {
		q := r.URL.Query()
		messages, err := hub.Messages(r.Context(), cu.User.ID, mailhub.Filter{View: q.Get("view"), Provider: q.Get("provider"), Account: q.Get("account"), Label: q.Get("label"), Query: q.Get("q")})
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		seeds := make([]store.MessageRecord, 0, len(messages))
		for _, m := range messages {
			row, e := s.store.GetMessageForUser(r.Context(), cu.User.ID, m.ID)
			if e != nil {
				s.serverError(w, r, e)
				return
			}
			seeds = append(seeds, row)
		}
		conversations, err := s.conversationViewsWithSearchSnippet(r.Context(), cu.User.ID, seeds, s.ownAddresses(r.Context(), cu.User), q.Get("q"))
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		census := []provider.Census{}
		for _, a := range provider.MockAccounts(cu.User.ID) {
			p, err := hub.Source(r.Context(), a)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			c, err := p.Census(r.Context(), a)
			if err != nil {
				s.serverError(w, r, err)
				return
			}
			census = append(census, c)
		}
		accounts, err := s.store.ListMailAccountsForUser(r.Context(), cu.User.ID)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		credentials := 0
		for _, account := range accounts {
			if account.EncryptedPassword != "" {
				credentials++
			}
		}
		writeJSON(w, map[string]any{"messages": messages, "conversations": apiConversations(conversations), "census": census, "real_credentials": credentials, "oauth_accounts": 0})
		return
	}
	if path != "mock-hub/sync" && path != "mock-hub/mutate" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if path == "mock-hub/sync" {
		for _, a := range provider.MockAccounts(cu.User.ID) {
			if err := hub.Sync(r.Context(), a); err != nil {
				s.serverError(w, r, err)
				return
			}
		}
	} else {
		var body struct {
			Account  string            `json:"account"`
			Mutation provider.Mutation `json:"mutation"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			writeAPIError(w, 400, "invalid mock mutation")
			return
		}
		if body.Account != "personal" && body.Account != "work" {
			writeAPIError(w, 400, "unknown mock account")
			return
		}
		a := provider.Account{UserID: cu.User.ID, ID: body.Account, Provider: provider.Mock}
		// Validate before journaling; only the fixed mock source can be changed.
		p, err := hub.Source(r.Context(), a)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		if err = p.Mutate(r.Context(), body.Mutation); err != nil {
			writeAPIError(w, 400, err.Error())
			return
		}
		if err = hub.Mutate(r.Context(), a, body.Mutation); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.noteMailListChanged(cu.User.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

// mutateMockMessage intercepts shared UI actions before any IMAP work is queued.
func (s *Server) mutateMockMessage(ctx context.Context, userID, messageID int64, action string, value bool) (bool, error) {
	s.mockHubMu.Lock()
	defer s.mockHubMu.Unlock()
	metadata, err := s.store.ProviderMessages(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, m := range metadata {
		if m.ID == messageID {
			hub := mailhub.Service{Store: s.store, Blobs: s.blobs, Search: s.search}
			return true, hub.Mutate(ctx, provider.Account{UserID: userID, ID: m.Account, Provider: provider.Mock}, provider.Mutation{MessageID: m.NativeID, Action: action, Value: value})
		}
	}
	return false, nil
}

func (s *Server) markMailRead(ctx context.Context, userID int64, ids []int64, read bool) ([]int64, error) {
	real := make([]int64, 0, len(ids))
	for _, id := range ids {
		mock, err := s.mutateMockMessage(ctx, userID, id, "read", read)
		if err != nil {
			return nil, err
		}
		if !mock {
			real = append(real, id)
		}
	}
	return real, s.store.MarkMessagesReadForUser(ctx, userID, real, read, true)
}
