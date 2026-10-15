package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newModelListServer(t *testing.T, expectedKey string, ids ...string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("model probe path=%q", r.URL.Path)
		}
		if expectedKey != "" && r.Header.Get("Authorization") != "Bearer "+expectedKey {
			t.Fatalf("model probe auth=%q", r.Header.Get("Authorization"))
		}
		data := make([]map[string]string, 0, len(ids))
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
	}))
}

func TestModelConfigPersistsAndNeverReturnsAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.json")
	apiKey := "model-key-must-not-leak"
	modelAPI := newModelListServer(t, apiKey, "real-test-model")
	defer modelAPI.Close()
	s := &server{cfg: config{Shared: "gate-f-secret", ModelConfigFile: path, HTTPClient: modelAPI.Client()}}
	body, _ := json.Marshal(modelConfigRequest{BaseURL: modelAPI.URL + "/v1/", Model: "real-test-model", APIKey: &apiKey})
	r := httptest.NewRequest(http.MethodPut, "/config", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer gate-f-secret")
	w := httptest.NewRecorder()
	s.modelConfigHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), apiKey) || !strings.Contains(w.Body.String(), `"api_key_configured":true`) {
		t.Fatalf("unsafe config response: %s", w.Body.String())
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode=%o", info.Mode().Perm())
	}

	reloaded := &server{cfg: config{Shared: "gate-f-secret", ModelConfigFile: path}}
	if err := reloaded.initModelConfig(); err != nil {
		t.Fatal(err)
	}
	get := httptest.NewRequest(http.MethodGet, "/config", nil)
	get.Header.Set("Authorization", "Bearer gate-f-secret")
	got := httptest.NewRecorder()
	reloaded.modelConfigHandler(got, get)
	if strings.Contains(got.Body.String(), apiKey) || !strings.Contains(got.Body.String(), `"model":"real-test-model"`) {
		t.Fatalf("unsafe reloaded response: %s", got.Body.String())
	}
}

func TestModelConfigRejectsUnknownModelWithoutReplacingWorkingConfig(t *testing.T) {
	modelAPI := newModelListServer(t, "", "hy3", "hy4-preview")
	defer modelAPI.Close()
	s := &server{cfg: config{Shared: "secret", HTTPClient: modelAPI.Client()}}
	s.setModelConfig("https://old.example.invalid/v1", "old-key", "old-model")
	r := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"base_url":"`+modelAPI.URL+`/v1","model":"wba","clear_api_key":true}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.modelConfigHandler(w, r)
	if w.Code != http.StatusBadGateway {
		t.Fatalf("unknown model status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `model "wba" is not available`) || !strings.Contains(w.Body.String(), "hy3") {
		t.Fatalf("missing useful model validation error: %s", w.Body.String())
	}
	baseURL, key, model := s.currentModelConfig()
	if baseURL != "https://old.example.invalid/v1" || key != "old-key" || model != "old-model" {
		t.Fatalf("failed validation replaced working config: %q %q %q", baseURL, key, model)
	}
}

func TestModelConfigAllowsUnconfiguredStartupAndRejectsUnsafeURL(t *testing.T) {
	if err := validateConfig(config{HubURL: "http://hub", Shared: "secret"}); err != nil {
		t.Fatalf("unconfigured model should allow startup: %v", err)
	}
	s := &server{cfg: config{Shared: "secret"}}
	r := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"base_url":"file:///tmp/model","model":"x"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.modelConfigHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unsafe base url status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestModelConfigRejectsUnreachableEndpoint(t *testing.T) {
	s := &server{cfg: config{Shared: "secret", HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, os.ErrNotExist
	})}}}
	r := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"base_url":"http://127.0.0.1:8643/v1","model":"hy3"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.modelConfigHandler(w, r)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "model endpoint validation failed") {
		t.Fatalf("unreachable endpoint status=%d body=%s", w.Code, w.Body.String())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestModelConfigDoesNotLeakSavedKeyToChangedBaseURL(t *testing.T) {
	modelAPI := newModelListServer(t, "", "hy3")
	defer modelAPI.Close()
	s := &server{cfg: config{Shared: "secret", HTTPClient: modelAPI.Client()}}
	s.setModelConfig("https://old.example.invalid/v1", "old-secret", "old-model")
	r := httptest.NewRequest(http.MethodPut, "/config", bytes.NewBufferString(`{"base_url":"`+modelAPI.URL+`/v1","model":"hy3"}`))
	r.Header.Set("Authorization", "Bearer secret")
	w := httptest.NewRecorder()
	s.modelConfigHandler(w, r)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "api_key must be re-entered") {
		t.Fatalf("base URL key safety status=%d body=%s", w.Code, w.Body.String())
	}
	baseURL, key, model := s.currentModelConfig()
	if baseURL != "https://old.example.invalid/v1" || key != "old-secret" || model != "old-model" {
		t.Fatalf("rejected base URL change mutated config: %q %q %q", baseURL, key, model)
	}
}
