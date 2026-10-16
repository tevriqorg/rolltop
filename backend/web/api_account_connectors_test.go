package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"rolltop/backend/accountconnector"
	"rolltop/backend/store"
)

type fakeGoogleConnector struct{}

func (fakeGoogleConnector) ID() string { return "google" }
func (fakeGoogleConnector) Scopes([]accountconnector.Capability) ([]string, error) {
	return []string{"openid", "email", "gmail.modify"}, nil
}
func (fakeGoogleConnector) AuthorizationURL(in accountconnector.AuthRequest) (string, error) {
	return "https://accounts.example.test/auth?state=" + url.QueryEscape(in.State), nil
}
func (fakeGoogleConnector) Exchange(context.Context, string, accountconnector.PKCE) (accountconnector.Grant, error) {
	return accountconnector.Grant{
		Identity: accountconnector.IdentityInfo{Subject: "subject-1", Email: "owner@gmail.test", EmailVerified: true, Name: "Owner"},
		Tokens:   accountconnector.TokenSet{AccessToken: "access", RefreshToken: "refresh-secret", Scopes: []string{"openid", "email", "gmail.modify"}, Expiry: time.Now().Add(time.Hour)},
	}, nil
}
func (fakeGoogleConnector) Refresh(context.Context, string) (accountconnector.TokenSet, error) {
	return accountconnector.TokenSet{}, nil
}
func TestGoogleConnectorHTTPStoresGrantWithoutStartingBackfill(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	s := &Server{
		store: st, masterKey: key, googleConnector: fakeGoogleConnector{},
		accountConnectors:  accountconnector.Service{Store: st, MasterKey: key},
		googleOAuthPending: make(map[string]googleOAuthPending),
	}
	request := func(method, path string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(nil))
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: owner}))
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "connector-csrf"})
			r.Header.Set("X-CSRF-Token", s.csrfForBase("connector-csrf"))
		}
		w := httptest.NewRecorder()
		s.handleAPI(w, r)
		return w
	}
	if w := request("POST", "/api/account-connectors/google/start", false); w.Code != http.StatusForbidden {
		t.Fatalf("missing csrf=%d", w.Code)
	}
	start := request("POST", "/api/account-connectors/google/start", true)
	if start.Code != http.StatusOK {
		t.Fatalf("start=%d %s", start.Code, start.Body.String())
	}
	var started map[string]string
	if err := json.Unmarshal(start.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	authURL, err := url.Parse(started["authorization_url"])
	if err != nil {
		t.Fatal(err)
	}
	state := authURL.Query().Get("state")
	if state == "" {
		t.Fatal("missing oauth state")
	}
	callback := request("GET", "/api/account-connectors/google/callback?code=ok&state="+url.QueryEscape(state), false)
	if callback.Code != http.StatusSeeOther {
		t.Fatalf("callback=%d %s", callback.Code, callback.Body.String())
	}
	connections, err := st.ListAccountConnections(ctx, owner.ID, "google")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 1 || connections[0].Email != "owner@gmail.test" {
		t.Fatalf("connections=%+v", connections)
	}
	if connections[0].EncryptedRefreshToken == "refresh-secret" {
		t.Fatal("plaintext refresh token stored")
	}
	status := request("GET", "/api/account-connectors/google", false)
	if status.Code != http.StatusOK {
		t.Fatalf("status=%d %s", status.Code, status.Body.String())
	}
	var body struct {
		Configured     bool  `json:"configured"`
		BackfillLocked bool  `json:"backfill_locked"`
		CensusRequired bool  `json:"census_required"`
		Connections    []any `json:"connections"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Configured || !body.BackfillLocked || !body.CensusRequired || len(body.Connections) != 1 {
		t.Fatalf("unsafe status=%s", status.Body.String())
	}
}
