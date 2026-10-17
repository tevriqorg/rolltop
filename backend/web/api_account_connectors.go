package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"rolltop/backend/accountconnector"
	"rolltop/backend/auth"
	"rolltop/backend/mailhub"
	"rolltop/backend/provider"
	gmailprovider "rolltop/backend/provider/gmail"
	"rolltop/backend/store"
)

type googleOAuthPending struct {
	UserID       int64
	PKCE         accountconnector.PKCE
	Capabilities []accountconnector.Capability
	ExpiresAt    time.Time
}

var gmailCapabilities = []accountconnector.Capability{
	accountconnector.Identity,
	accountconnector.MailRead,
	accountconnector.MailModify,
	accountconnector.MailSend,
}

type googleCensusStatus struct {
	MessageCount          int       `json:"message_count"`
	EstimatedRawBytes     int64     `json:"estimated_raw_bytes"`
	ExpectedEvidenceBytes int64     `json:"expected_evidence_bytes"`
	RecommendedFreeDisk   int64     `json:"recommended_free_disk"`
	Method                string    `json:"method"`
	Exact                 bool      `json:"exact"`
	CompletedAt           time.Time `json:"completed_at"`
}

type googleConnectionStatus struct {
	Subject          string              `json:"subject"`
	Email            string              `json:"email"`
	DisplayName      string              `json:"display_name"`
	Scopes           []string            `json:"scopes"`
	Capabilities     []string            `json:"capabilities"`
	Census           *googleCensusStatus `json:"census,omitempty"`
	BackfillApproved bool                `json:"backfill_approved"`
	BackfillLocked   bool                `json:"backfill_locked"`
}

func (s *Server) apiGoogleConnector(w http.ResponseWriter, r *http.Request, path string) {
	cu, ok := s.requireAPIAuth(w, r)
	if !ok {
		return
	}
	switch path {
	case "account-connectors/google":
		s.googleConnectorStatus(w, r, cu.User.ID)
	case "account-connectors/google/start":
		s.googleConnectorStart(w, r, cu.User.ID)
	case "account-connectors/google/callback":
		s.googleConnectorCallback(w, r, cu.User.ID)
	case "account-connectors/google/census":
		s.googleConnectorCensus(w, r, cu.User.ID)
	case "account-connectors/google/backfill/approve":
		s.googleConnectorApproveBackfill(w, r, cu.User.ID)
	case "account-connectors/google/sync":
		s.googleConnectorSync(w, r, cu.User.ID)
	default:
		http.NotFound(w, r)
	}
}

func googleSourceID(connection store.AccountConnection) string {
	return "google-" + strconv.FormatInt(connection.ID, 10)
}

func (s *Server) googleConnectorStatus(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.googleConnector == nil {
		writeJSON(w, map[string]any{"configured": false, "connections": []any{}, "census_required": false, "backfill_locked": true})
		return
	}
	connections, err := s.store.ListAccountConnections(r.Context(), userID, s.googleConnector.ID())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	out := make([]googleConnectionStatus, 0, len(connections))
	censusRequired := len(connections) > 0
	allApproved := len(connections) == 1
	for _, connection := range connections {
		item := googleConnectionStatus{
			Subject: connection.ExternalSubject, Email: connection.Email, DisplayName: connection.DisplayName,
			Scopes: connection.Scopes, Capabilities: connection.Capabilities, BackfillLocked: true,
		}
		gate, gateErr := s.store.GetProviderSyncGate(r.Context(), userID, string(provider.Gmail), googleSourceID(connection))
		if gateErr == nil {
			if gate.HasCensus() {
				item.Census = &googleCensusStatus{
					MessageCount: gate.CensusMessageCount, EstimatedRawBytes: gate.CensusEstimatedRaw,
					ExpectedEvidenceBytes: gate.CensusExpectedEvidence, RecommendedFreeDisk: gate.CensusRecommendedFree,
					Method: gate.CensusMethod, Exact: gate.CensusExact, CompletedAt: gate.CensusAt,
				}
				censusRequired = false
			}
			item.BackfillApproved = gate.BackfillApproved()
			item.BackfillLocked = !item.BackfillApproved
		} else if !store.IsNotFound(gateErr) {
			s.serverError(w, r, gateErr)
			return
		}
		if !item.BackfillApproved {
			allApproved = false
		}
		out = append(out, item)
	}
	writeJSON(w, map[string]any{
		"configured": true, "connections": out, "census_required": censusRequired,
		"backfill_locked": !allApproved,
	})
}

func (s *Server) googleConnectorStart(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	if s.googleConnector == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "Google connector is not configured.")
		return
	}
	state, err := auth.NewOpaqueToken()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pkce, err := accountconnector.NewPKCE()
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	pending := googleOAuthPending{UserID: userID, PKCE: pkce, Capabilities: append([]accountconnector.Capability(nil), gmailCapabilities...), ExpiresAt: time.Now().Add(10 * time.Minute)}
	s.googleOAuthMu.Lock()
	for key, item := range s.googleOAuthPending {
		if time.Now().After(item.ExpiresAt) {
			delete(s.googleOAuthPending, key)
		}
	}
	s.googleOAuthPending[state] = pending
	s.googleOAuthMu.Unlock()
	authorizationURL, err := s.googleConnector.AuthorizationURL(accountconnector.AuthRequest{
		State: state, PKCE: pkce, Capabilities: pending.Capabilities, ForceConsent: true,
	})
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, map[string]string{"authorization_url": authorizationURL})
}

func (s *Server) googleConnectorCallback(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	state := strings.TrimSpace(r.URL.Query().Get("state"))
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if oauthErr := strings.TrimSpace(r.URL.Query().Get("error")); oauthErr != "" {
		http.Redirect(w, r, "/settings/account/mail?google=denied", http.StatusSeeOther)
		return
	}
	if state == "" || code == "" {
		writeAPIError(w, http.StatusBadRequest, "Google OAuth callback is missing code or state.")
		return
	}
	s.googleOAuthMu.Lock()
	pending, found := s.googleOAuthPending[state]
	delete(s.googleOAuthPending, state)
	s.googleOAuthMu.Unlock()
	if !found || pending.UserID != userID || time.Now().After(pending.ExpiresAt) {
		writeAPIError(w, http.StatusBadRequest, "Google OAuth state is invalid or expired.")
		return
	}
	connection, _, err := s.accountConnectors.FinishAuthorization(r.Context(), userID, s.googleConnector, pending.Capabilities, code, pending.PKCE)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if _, err := s.store.CreateMailIdentityForUser(r.Context(), userID, store.MailIdentity{Email: connection.Email, DisplayName: connection.DisplayName, AutocryptEnabled: true}); err != nil {
		s.serverError(w, r, err)
		return
	}
	s.clearComposeIdentityCache(userID)
	// Authorization never unlocks or starts Gmail backfill. The next real API
	// operation is forced through metadata-only Census.
	http.Redirect(w, r, "/settings/account/mail?google=connected", http.StatusSeeOther)
}

func (s *Server) googleG1Connection(ctx context.Context, userID int64) (store.AccountConnection, error) {
	if s.googleConnector == nil {
		return store.AccountConnection{}, errors.New("Google connector is not configured")
	}
	connections, err := s.store.ListAccountConnections(ctx, userID, s.googleConnector.ID())
	if err != nil {
		return store.AccountConnection{}, err
	}
	if len(connections) != 1 {
		return store.AccountConnection{}, errors.New("Gate G1 requires exactly one connected Google account")
	}
	return connections[0], nil
}

func (s *Server) gmailClientForConnection(ctx context.Context, userID int64, connection store.AccountConnection) (gmailprovider.Client, error) {
	tokens, err := s.accountConnectors.RefreshAccess(ctx, userID, s.googleConnector, connection.ExternalSubject)
	if err != nil {
		return gmailprovider.Client{}, err
	}
	client := gmailprovider.Client{BaseURL: s.gmailAPIBaseURL, AccessToken: tokens.AccessToken, HTTPClient: s.gmailHTTPClient}
	if s.syncer != nil {
		client.MaxRawMessageBytes = s.syncer.MaxMessageBytes
	}
	return client, nil
}

func googleProviderAccount(userID int64, connection store.AccountConnection) provider.Account {
	return provider.Account{UserID: userID, ID: googleSourceID(connection), Provider: provider.Gmail}
}

func (s *Server) googleConnectorCensus(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	connection, err := s.googleG1Connection(r.Context(), userID)
	if err != nil {
		writeAPIError(w, http.StatusConflict, err.Error()+".")
		return
	}
	client, err := s.gmailClientForConnection(r.Context(), userID, connection)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	account := googleProviderAccount(userID, connection)
	census, err := client.Census(r.Context(), account)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	gate, err := s.store.SaveProviderCensus(r.Context(), userID, string(provider.Gmail), account.ID,
		census.MessageCount, census.EstimatedRawBytes, census.ExpectedEvidenceBytes,
		census.RecommendedFreeDisk, census.Method, census.Exact)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	writeJSON(w, map[string]any{
		"census": census, "account_email": connection.Email,
		"completed_at": gate.CensusAt, "backfill_locked": true,
	})
}

func (s *Server) googleConnectorApproveBackfill(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil || !in.Confirm {
		writeAPIError(w, http.StatusBadRequest, "Explicit backfill approval is required.")
		return
	}
	connection, err := s.googleG1Connection(r.Context(), userID)
	if err != nil {
		writeAPIError(w, http.StatusConflict, err.Error()+".")
		return
	}
	gate, err := s.store.ApproveProviderBackfill(r.Context(), userID, string(provider.Gmail), googleSourceID(connection))
	if err != nil {
		writeAPIError(w, http.StatusConflict, err.Error()+".")
		return
	}
	writeJSON(w, map[string]any{"ok": true, "backfill_locked": false, "approved_at": gate.BackfillApprovedAt})
}

func (s *Server) syncApprovedGmail(ctx context.Context, userID int64, connection store.AccountConnection) error {
	account := googleProviderAccount(userID, connection)
	gate, err := s.store.GetProviderSyncGate(ctx, userID, string(provider.Gmail), account.ID)
	if err != nil {
		return err
	}
	if !gate.BackfillApproved() {
		return errors.New("Gmail backfill remains locked pending Census review and explicit approval")
	}
	client, err := s.gmailClientForConnection(ctx, userID, connection)
	if err != nil {
		return err
	}
	hub := mailhub.Service{Store: s.store, Blobs: s.blobs, Search: s.search}
	s.providerHubMu.Lock()
	defer s.providerHubMu.Unlock()
	return hub.SyncGmail(ctx, account, connection.DisplayName, connection.Email, client)
}

func (s *Server) googleConnectorSync(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !s.verifyCSRF(w, r) {
		return
	}
	connection, err := s.googleG1Connection(r.Context(), userID)
	if err != nil {
		writeAPIError(w, http.StatusConflict, err.Error()+".")
		return
	}
	if err := s.syncApprovedGmail(r.Context(), userID, connection); err != nil {
		if store.IsNotFound(err) || strings.Contains(err.Error(), "locked") {
			writeAPIError(w, http.StatusConflict, err.Error()+".")
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.noteMailListChanged(userID)
	writeJSON(w, map[string]any{"ok": true, "account_email": connection.Email, "backfill_locked": false})
}
