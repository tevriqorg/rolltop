package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type mailAIModelConfig struct {
	Configured       bool   `json:"configured"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	APIKeyConfigured bool   `json:"api_key_configured"`
	CanConfigure     bool   `json:"can_configure,omitempty"`
}

type mailAIModelConfigUpdate struct {
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	APIKey      *string `json:"api_key,omitempty"`
	ClearAPIKey bool    `json:"clear_api_key,omitempty"`
}

func (s *Server) apiMailAIModelConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if s.mailAIURL == "" || s.mailAISharedToken == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is not configured")
		return
	}
	switch r.Method {
	case http.MethodGet:
		cu, ok := s.requireAPIAuth(w, r)
		if !ok {
			return
		}
		cfg, err := s.mailAIModelConfigRequest(r.Context(), http.MethodGet, nil)
		if err != nil {
			writeAPIError(w, http.StatusServiceUnavailable, "mail-ai is unavailable")
			return
		}
		cfg.CanConfigure = cu.User.IsAdmin
		writeJSON(w, cfg)
	case http.MethodPut:
		if _, ok := s.requireAPIAdmin(w, r); !ok {
			return
		}
		if !s.verifyCSRF(w, r) {
			return
		}
		var body mailAIModelConfigUpdate
		if !decodeMailAIJSON(w, r, &body, 32<<10) {
			return
		}
		if strings.TrimSpace(body.BaseURL) == "" || strings.TrimSpace(body.Model) == "" {
			writeAPIError(w, http.StatusBadRequest, "base_url and model are required")
			return
		}
		cfg, err := s.mailAIModelConfigRequest(r.Context(), http.MethodPut, body)
		if err != nil {
			writeAPIError(w, http.StatusBadGateway, err.Error())
			return
		}
		writeJSON(w, cfg)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) mailAIModelConfigRequest(ctx context.Context, method string, payload any) (mailAIModelConfig, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return mailAIModelConfig{}, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.mailAIURL+"/config", body)
	if err != nil {
		return mailAIModelConfig{}, err
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+s.mailAISharedToken)
	res, err := s.mailAIHTTPClient.Do(req)
	if err != nil {
		return mailAIModelConfig{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		if strings.TrimSpace(string(message)) == "" {
			return mailAIModelConfig{}, fmt.Errorf("mail-ai config status %d", res.StatusCode)
		}
		return mailAIModelConfig{}, errors.New(strings.TrimSpace(string(message)))
	}
	var out mailAIModelConfig
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<10)).Decode(&out); err != nil {
		return mailAIModelConfig{}, err
	}
	return out, nil
}
