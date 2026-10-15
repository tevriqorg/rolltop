package web

import (
	"net/http"
	"strings"
	"time"

	"rolltop/backend/accountconnector"
	"rolltop/backend/auth"
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
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) googleConnectorStatus(w http.ResponseWriter, r *http.Request, userID int64) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.googleConnector == nil {
		writeJSON(w, map[string]any{"configured": false, "connections": []any{}, "backfill_locked": true})
		return
	}
	connections, err := s.store.ListAccountConnections(r.Context(), userID, s.googleConnector.ID())
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	type safeConnection struct {
		Subject      string   `json:"subject"`
		Email        string   `json:"email"`
		DisplayName  string   `json:"display_name"`
		Scopes       []string `json:"scopes"`
		Capabilities []string `json:"capabilities"`
	}
	out := make([]safeConnection, 0, len(connections))
	for _, connection := range connections {
		out = append(out, safeConnection{
			Subject: connection.ExternalSubject, Email: connection.Email, DisplayName: connection.DisplayName,
			Scopes: connection.Scopes, Capabilities: connection.Capabilities,
		})
	}
	writeJSON(w, map[string]any{"configured": true, "connections": out, "census_required": len(out) > 0, "backfill_locked": true})
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
	_, _, err := s.accountConnectors.FinishAuthorization(r.Context(), userID, s.googleConnector, pending.Capabilities, code, pending.PKCE)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	// Connecting an account never starts a Gmail backfill. Gate G1 requires a
	// real Storage Census and explicit approval before any message download.
	http.Redirect(w, r, "/settings/account/mail?google=connected", http.StatusSeeOther)
}
