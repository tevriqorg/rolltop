package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"rolltop/backend/store"
)

func TestMailAIModelConfigIsAdminOnlyAndNeverReturnsAPIKey(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "mail.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	admin, _ := st.CreateUser(ctx, "ai-admin@example.invalid", "AI Admin", "hash", true)
	user, _ := st.CreateUser(ctx, "ai-user@example.invalid", "AI User", "hash", false)
	const secret = "real-model-key"
	var received mailAIModelConfigUpdate
	putCalls := 0
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gate-f-secret" {
			t.Fatalf("sidecar auth=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPut {
			putCalls++
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
		}
		_, _ = w.Write([]byte(`{"configured":true,"base_url":"https://api.example.invalid/v1","model":"real-model","api_key_configured":true}`))
	}))
	defer sidecar.Close()
	s := &Server{store: st, masterKey: []byte("12345678901234567890123456789012"), mailAIURL: sidecar.URL, mailAISharedToken: "gate-f-secret", mailAIHTTPClient: sidecar.Client()}

	body := `{"base_url":"https://api.example.invalid/v1","model":"real-model","api_key":"` + secret + `"}`
	put := func(actor store.User, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/mail-ai/model-config", bytes.NewBufferString(body))
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: actor}))
		r.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
		r.Header.Set("X-CSRF-Token", s.csrfForBase(csrf))
		w := httptest.NewRecorder()
		s.apiMailAIModelConfig(w, r)
		return w
	}
	if w := put(admin, "admin-csrf"); w.Code != http.StatusOK || strings.Contains(w.Body.String(), secret) {
		t.Fatalf("admin config=%d body=%s", w.Code, w.Body.String())
	}
	if received.APIKey == nil || *received.APIKey != secret || putCalls != 1 {
		t.Fatalf("sidecar received=%+v put_calls=%d", received, putCalls)
	}
	if w := put(user, "user-csrf"); w.Code != http.StatusForbidden || putCalls != 1 {
		t.Fatalf("non-admin config=%d put_calls=%d body=%s", w.Code, putCalls, w.Body.String())
	}

	get := func(actor store.User) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodGet, "/api/mail-ai/model-config", nil)
		r = r.WithContext(context.WithValue(r.Context(), userContextKey, currentUser{User: actor}))
		w := httptest.NewRecorder()
		s.apiMailAIModelConfig(w, r)
		return w
	}
	if w := get(admin); w.Code != http.StatusOK || strings.Contains(w.Body.String(), secret) || !strings.Contains(w.Body.String(), `"can_configure":true`) {
		t.Fatalf("admin get=%d body=%s", w.Code, w.Body.String())
	}
	if w := get(user); w.Code != http.StatusOK || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), `"can_configure":true`) {
		t.Fatalf("user get=%d body=%s", w.Code, w.Body.String())
	}
}
