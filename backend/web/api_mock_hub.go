package web

import (
	"context"
	"encoding/json"
	"errors"
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
	s.providerHubMu.Lock()
	defer s.providerHubMu.Unlock()
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
		for _, a := range mailhub.Accounts(cu.User.ID) {
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
		for _, a := range mailhub.Accounts(cu.User.ID) {
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
		switch body.Mutation.Action {
		case "read", "star", "label", "archive", "trash":
		default:
			writeAPIError(w, 400, "unsupported mock mutation")
			return
		}
		a := provider.Account{UserID: cu.User.ID, ID: body.Account, Provider: provider.Mock}
		if err := hub.Mutate(r.Context(), a, body.Mutation); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.noteMailListChanged(cu.User.ID)
	writeJSON(w, map[string]bool{"ok": true})
}

// mutateProviderMessage intercepts provider-native shared UI actions before any
// IMAP work is queued. Source mutation is committed before Mirror projection;
// the provider cursor advances only after projection succeeds.
func (s *Server) mutateProviderMessage(ctx context.Context, userID, messageID int64, change provider.Mutation) (bool, error) {
	meta, err := s.store.ProviderMessageForLocalID(ctx, userID, messageID)
	if store.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	change.MessageID = meta.NativeID
	hub := mailhub.Service{Store: s.store, Blobs: s.blobs, Search: s.search}
	s.providerHubMu.Lock()
	defer s.providerHubMu.Unlock()
	switch meta.Provider {
	case string(provider.Mock):
		return true, hub.Mutate(ctx, provider.Account{UserID: userID, ID: meta.Account, Provider: provider.Mock}, change)
	case string(provider.Gmail):
		if s.googleConnector == nil {
			return true, errors.New("Google connector is not configured")
		}
		connections, err := s.store.ListAccountConnections(ctx, userID, s.googleConnector.ID())
		if err != nil {
			return true, err
		}
		var connection store.AccountConnection
		for _, candidate := range connections {
			if googleSourceID(candidate) == meta.Account {
				connection = candidate
				break
			}
		}
		if connection.ID == 0 {
			return true, errors.New("Gmail account connection is unavailable")
		}
		gate, err := s.store.GetProviderSyncGate(ctx, userID, string(provider.Gmail), meta.Account)
		if err != nil {
			return true, err
		}
		if !gate.BackfillApproved() {
			return true, errors.New("Gmail mutations remain locked pending Census review and explicit approval")
		}
		client, err := s.gmailClientForConnection(ctx, userID, connection)
		if err != nil {
			return true, err
		}
		return true, hub.MutateGmail(ctx, googleProviderAccount(userID, connection), connection.DisplayName, connection.Email, client, change)
	default:
		return true, errors.New("unsupported provider mutation")
	}
}

func (s *Server) markMailRead(ctx context.Context, userID int64, ids []int64, read bool) ([]int64, error) {
	real := make([]int64, 0, len(ids))
	for _, id := range ids {
		native, err := s.mutateProviderMessage(ctx, userID, id, provider.Mutation{Action: "read", Value: read})
		if err != nil {
			return nil, err
		}
		if !native {
			real = append(real, id)
		}
	}
	return real, s.store.MarkMessagesReadForUser(ctx, userID, real, read, true)
}
