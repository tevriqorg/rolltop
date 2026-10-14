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

func TestModelConfigPersistsAndNeverReturnsAPIKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.json")
	s := &server{cfg: config{Shared: "gate-f-secret", ModelConfigFile: path}}
	apiKey := "model-key-must-not-leak"
	body, _ := json.Marshal(modelConfigRequest{BaseURL: "https://models.example.invalid/v1/", Model: "real-test-model", APIKey: &apiKey})
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
