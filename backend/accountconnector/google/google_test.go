package google

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"rolltop/backend/accountconnector"
)

func testConfig(server string) Config {
	return Config{
		ClientID: "client-id", ClientSecret: "client-secret",
		RedirectURL:           "http://127.0.0.1/callback",
		AuthorizationEndpoint: server + "/authorize",
		TokenEndpoint:         server + "/token",
		UserinfoEndpoint:      server + "/userinfo",
	}
}

func TestScopesAreMinimalAndDeterministic(t *testing.T) {
	c, err := New(testConfig("http://127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		caps []accountconnector.Capability
		want []string
	}{
		{"identity", []accountconnector.Capability{accountconnector.Identity}, []string{"openid", "email"}},
		{"read", []accountconnector.Capability{accountconnector.MailRead}, []string{"openid", "email", gmailReadonlyScope}},
		{"read modify", []accountconnector.Capability{accountconnector.MailRead, accountconnector.MailModify}, []string{"openid", "email", gmailModifyScope}},
		{"modify send", []accountconnector.Capability{accountconnector.MailSend, accountconnector.MailModify}, []string{"openid", "email", gmailModifyScope, gmailSendScope}},
		{"send only", []accountconnector.Capability{accountconnector.MailSend}, []string{"openid", "email", gmailSendScope}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Scopes(tc.caps)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("scopes=%v want=%v", got, tc.want)
			}
		})
	}
	if _, err := c.Scopes([]accountconnector.Capability{"unknown"}); err == nil {
		t.Fatal("unknown capability was accepted")
	}
}

func TestAuthorizationURLUsesPKCEOfflineAndNeverSecret(t *testing.T) {
	c, err := New(testConfig("http://127.0.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	pkce, err := accountconnector.NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.AuthorizationURL(accountconnector.AuthRequest{
		State: "state-1", PKCE: pkce,
		Capabilities: []accountconnector.Capability{accountconnector.MailModify, accountconnector.MailSend},
		ForceConsent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	checks := map[string]string{
		"client_id": "client-id", "redirect_uri": "http://127.0.0.1/callback",
		"state": "state-1", "code_challenge": pkce.Challenge,
		"code_challenge_method": "S256", "access_type": "offline",
		"include_granted_scopes": "true", "prompt": "consent",
	}
	for key, want := range checks {
		if got := q.Get(key); got != want {
			t.Fatalf("%s=%q want=%q", key, got, want)
		}
	}
	if strings.Contains(raw, "client-secret") || q.Get("client_secret") != "" {
		t.Fatal("authorization URL exposed client secret")
	}
	if !strings.Contains(q.Get("scope"), gmailModifyScope) || !strings.Contains(q.Get("scope"), gmailSendScope) {
		t.Fatalf("scope=%q", q.Get("scope"))
	}
}

func TestExchangeAndRefreshLifecycle(t *testing.T) {
	fixed := time.Date(2026, 10, 15, 3, 0, 0, 0, time.UTC)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("client_secret") != "client-secret" {
				t.Error("missing client secret")
			}
			switch r.Form.Get("grant_type") {
			case "authorization_code":
				if r.Form.Get("code") != "code-1" || r.Form.Get("code_verifier") != "verifier-1" {
					t.Error("authorization exchange values mismatch")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "access-1", "refresh_token": "refresh-1",
					"token_type": "Bearer", "expires_in": 3600,
					"scope": "openid email " + gmailModifyScope,
				})
			case "refresh_token":
				if r.Form.Get("refresh_token") != "refresh-1" {
					t.Error("refresh token mismatch")
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"access_token": "access-2", "token_type": "Bearer", "expires_in": 1800,
					"scope": "openid email " + gmailModifyScope,
				})
			default:
				w.WriteHeader(http.StatusBadRequest)
			}
		case "/userinfo":
			if r.Header.Get("Authorization") != "Bearer access-1" {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sub": "google-subject", "email": "Person@Example.com",
				"email_verified": true, "name": "Person",
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	cfg := testConfig(server.URL)
	cfg.Now = func() time.Time { return fixed }
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := c.Exchange(context.Background(), "code-1", accountconnector.PKCE{Verifier: "verifier-1", Challenge: "challenge-1"})
	if err != nil {
		t.Fatal(err)
	}
	if grant.Identity.Subject != "google-subject" || grant.Identity.Email != "person@example.com" || !grant.Identity.EmailVerified {
		t.Fatalf("identity=%+v", grant.Identity)
	}
	if grant.Tokens.AccessToken != "access-1" || grant.Tokens.RefreshToken != "refresh-1" || !grant.Tokens.Expiry.Equal(fixed.Add(time.Hour)) {
		t.Fatalf("tokens=%+v", grant.Tokens)
	}
	refreshed, err := c.Refresh(context.Background(), "refresh-1")
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "access-2" || refreshed.RefreshToken != "refresh-1" || !refreshed.Expiry.Equal(fixed.Add(30*time.Minute)) {
		t.Fatalf("refreshed=%+v", refreshed)
	}
}

func TestExchangeRejectsUnverifiedEmail(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "access", "refresh_token": "refresh", "expires_in": 3600})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"sub": "subject", "email": "person@example.com", "email_verified": false})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	c, err := New(testConfig(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exchange(context.Background(), "code", accountconnector.PKCE{Verifier: "verifier", Challenge: "challenge"})
	if err == nil || !strings.Contains(err.Error(), "not verified") {
		t.Fatalf("err=%v", err)
	}
}

func TestDefaultClientRefusesTokenRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("redirect target should not be reached")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	cfg := testConfig(redirector.URL)
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Exchange(context.Background(), "code", accountconnector.PKCE{Verifier: "verifier", Challenge: "challenge"})
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Fatalf("err=%v", err)
	}
}
