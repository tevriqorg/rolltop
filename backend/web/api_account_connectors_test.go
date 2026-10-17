package web

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rolltop/backend/accountconnector"
	"rolltop/backend/blob"
	"rolltop/backend/search"
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
	return accountconnector.TokenSet{AccessToken: "access", Scopes: []string{"openid", "email", "gmail.modify"}}, nil
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

func TestGoogleCensusApprovalGateAndSyntheticSync(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	index, err := search.Open(filepath.Join(dir, "bleve"))
	if err != nil {
		t.Fatal(err)
	}
	defer index.Close()
	raw := []byte("From: Sender <sender@example.test>\r\nTo: owner@gmail.test\r\nSubject: Gmail gate\r\nMessage-ID: <gmail-gate@example.test>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nbody from gmail\r\n")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Errorf("auth=%q", r.Header.Get("Authorization"))
		}
		switch {
		case r.URL.Path == "/users/me/profile":
			_ = json.NewEncoder(w).Encode(map[string]any{"emailAddress": "owner@gmail.test", "historyId": "101"})
		case r.URL.Path == "/users/me/labels":
			_ = json.NewEncoder(w).Encode(map[string]any{"labels": []map[string]string{{"id": "INBOX", "name": "INBOX"}, {"id": "UNREAD", "name": "UNREAD"}, {"id": "L1", "name": "Project"}}})
		case r.URL.Path == "/users/me/messages":
			_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "m1", "threadId": "t1"}}})
		case r.URL.Path == "/users/me/messages/m1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id": "m1", "threadId": "t1", "historyId": "101", "labelIds": []string{"INBOX", "UNREAD", "L1"},
				"sizeEstimate": len(raw), "raw": base64.RawURLEncoding.EncodeToString(raw),
				"payload": map[string]any{"headers": []map[string]string{{"name": "Message-ID", "value": "<gmail-gate@example.test>"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	key := []byte("12345678901234567890123456789012")
	s := &Server{
		store: st, blobs: blob.New(dir), search: index, masterKey: key,
		googleConnector: fakeGoogleConnector{}, accountConnectors: accountconnector.Service{Store: st, MasterKey: key},
		googleOAuthPending: make(map[string]googleOAuthPending), gmailAPIBaseURL: api.URL, gmailHTTPClient: api.Client(),
	}
	if _, _, err := s.accountConnectors.FinishAuthorization(ctx, owner.ID, s.googleConnector, gmailCapabilities, "ok", accountconnector.PKCE{}); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, csrf bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: owner}))
		if csrf {
			r.AddCookie(&http.Cookie{Name: csrfCookie, Value: "gate-csrf"})
			r.Header.Set("X-CSRF-Token", s.csrfForBase("gate-csrf"))
		}
		w := httptest.NewRecorder()
		s.handleAPI(w, r)
		return w
	}
	if w := request("POST", "/api/account-connectors/google/sync", "", true); w.Code != http.StatusConflict {
		t.Fatalf("pre-census sync=%d %s", w.Code, w.Body.String())
	}
	census := request("POST", "/api/account-connectors/google/census", "", true)
	if census.Code != http.StatusOK || !strings.Contains(census.Body.String(), `"backfill_locked":true`) {
		t.Fatalf("census=%d %s", census.Code, census.Body.String())
	}
	status := request("GET", "/api/account-connectors/google", "", false)
	if status.Code != http.StatusOK || !strings.Contains(status.Body.String(), `"message_count":1`) || !strings.Contains(status.Body.String(), `"backfill_approved":false`) {
		t.Fatalf("status=%d %s", status.Code, status.Body.String())
	}
	if w := request("POST", "/api/account-connectors/google/backfill/approve", `{"confirm":false}`, true); w.Code != http.StatusBadRequest {
		t.Fatalf("false approval=%d %s", w.Code, w.Body.String())
	}
	approved := request("POST", "/api/account-connectors/google/backfill/approve", `{"confirm":true}`, true)
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"backfill_locked":false`) {
		t.Fatalf("approval=%d %s", approved.Code, approved.Body.String())
	}
	synced := request("POST", "/api/account-connectors/google/sync", "", true)
	if synced.Code != http.StatusOK {
		t.Fatalf("sync=%d %s", synced.Code, synced.Body.String())
	}
	messages, err := st.ProviderMessagesFor(ctx, owner.ID, "gmail")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].NativeID != "m1" || messages[0].ThreadID != "t1" {
		t.Fatalf("messages=%+v", messages)
	}
	row, err := st.GetMessageForUser(ctx, owner.ID, messages[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.Subject != "Gmail gate" || row.IsRead {
		t.Fatalf("row=%+v", row)
	}
	// Re-running Census is a deliberate safety boundary: it invalidates an old approval.
	if w := request("POST", "/api/account-connectors/google/census", "", true); w.Code != http.StatusOK {
		t.Fatalf("recensus=%d %s", w.Code, w.Body.String())
	}
	if w := request("POST", "/api/account-connectors/google/sync", "", true); w.Code != http.StatusConflict {
		t.Fatalf("sync after recensus=%d %s", w.Code, w.Body.String())
	}
}
