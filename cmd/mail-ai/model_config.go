package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type modelRuntime struct {
	mu      sync.RWMutex
	baseURL string
	apiKey  string
	model   string
}

type modelConfigRequest struct {
	BaseURL     string  `json:"base_url"`
	Model       string  `json:"model"`
	APIKey      *string `json:"api_key,omitempty"`
	ClearAPIKey bool    `json:"clear_api_key,omitempty"`
}

type modelConfigResponse struct {
	Configured       bool   `json:"configured"`
	BaseURL          string `json:"base_url"`
	Model            string `json:"model"`
	APIKeyConfigured bool   `json:"api_key_configured"`
}

func normalizeModelConfig(baseURL, model string) (string, string, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	model = strings.TrimSpace(model)
	if baseURL == "" || model == "" {
		return "", "", errors.New("base_url and model are required")
	}
	u, err := url.Parse(baseURL)
	if err != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", "", errors.New("base_url must be an absolute http(s) URL")
	}
	if u.User != nil || u.Fragment != "" || u.RawQuery != "" {
		return "", "", errors.New("base_url must not contain credentials, query parameters, or fragments")
	}
	return baseURL, model, nil
}

func (s *server) currentModelConfig() (baseURL, apiKey, model string) {
	s.model.mu.RLock()
	defer s.model.mu.RUnlock()
	return s.model.baseURL, s.model.apiKey, s.model.model
}

func (s *server) setModelConfig(baseURL, apiKey, model string) {
	s.model.mu.Lock()
	defer s.model.mu.Unlock()
	s.model.baseURL = baseURL
	s.model.apiKey = apiKey
	s.model.model = model
}

func (s *server) modelConfigResponse() modelConfigResponse {
	baseURL, apiKey, model := s.currentModelConfig()
	return modelConfigResponse{
		Configured:       baseURL != "" && model != "",
		BaseURL:          baseURL,
		Model:            model,
		APIKeyConfigured: apiKey != "",
	}
}

func (s *server) validateModelEndpoint(ctx context.Context, baseURL, apiKey, model string) error {
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, baseURL+"/models", nil)
	if err != nil {
		return fmt.Errorf("validate model endpoint: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := s.cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 8 * time.Second}
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("model endpoint validation failed: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		detail := strings.TrimSpace(string(message))
		if detail == "" {
			detail = http.StatusText(res.StatusCode)
		}
		return fmt.Errorf("model endpoint validation status %d: %s", res.StatusCode, detail)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return fmt.Errorf("model endpoint returned invalid /models response: %w", err)
	}
	available := make([]string, 0, len(out.Data))
	for _, item := range out.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			continue
		}
		available = append(available, id)
		if id == model {
			return nil
		}
	}
	if len(available) == 0 {
		return errors.New("model endpoint returned no model IDs")
	}
	if len(available) > 8 {
		available = available[:8]
	}
	return fmt.Errorf("model %q is not available; endpoint models: %s", model, strings.Join(available, ", "))
}

func (s *server) initModelConfig() error {
	baseURL, model := s.cfg.ModelURL, s.cfg.Model
	apiKey := s.cfg.ModelKey
	path := strings.TrimSpace(s.cfg.ModelConfigFile)
	if path != "" {
		data, err := os.ReadFile(path)
		if err == nil {
			var stored struct {
				BaseURL string `json:"base_url"`
				APIKey  string `json:"api_key"`
				Model   string `json:"model"`
			}
			if err := json.Unmarshal(data, &stored); err != nil {
				return fmt.Errorf("decode mail-ai model config: %w", err)
			}
			baseURL, apiKey, model = stored.BaseURL, stored.APIKey, stored.Model
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read mail-ai model config: %w", err)
		}
	}
	if strings.TrimSpace(baseURL) == "" && strings.TrimSpace(model) == "" {
		s.setModelConfig("", strings.TrimSpace(apiKey), "")
		return nil
	}
	normalizedURL, normalizedModel, err := normalizeModelConfig(baseURL, model)
	if err != nil {
		return fmt.Errorf("validate mail-ai model config: %w", err)
	}
	s.setModelConfig(normalizedURL, strings.TrimSpace(apiKey), normalizedModel)
	return nil
}

func (s *server) persistModelConfig(baseURL, apiKey, model string) error {
	path := strings.TrimSpace(s.cfg.ModelConfigFile)
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mail-ai-model-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	enc := json.NewEncoder(tmp)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(map[string]string{"base_url": baseURL, "api_key": apiKey, "model": model}); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func (s *server) modelConfigHandler(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.modelConfigResponse())
	case http.MethodPut:
		var in modelConfigRequest
		if err := decodeLimited(r, &in, 32<<10); err != nil {
			http.Error(w, "invalid model config", http.StatusBadRequest)
			return
		}
		baseURL, model, err := normalizeModelConfig(in.BaseURL, in.Model)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		currentBaseURL, currentKey, _ := s.currentModelConfig()
		apiKey := currentKey
		if in.APIKey != nil {
			apiKey = strings.TrimSpace(*in.APIKey)
		} else if in.ClearAPIKey {
			apiKey = ""
		} else if currentKey != "" && currentBaseURL != "" && currentBaseURL != baseURL {
			http.Error(w, "api_key must be re-entered or explicitly cleared when base_url changes", http.StatusBadRequest)
			return
		}
		if err := s.validateModelEndpoint(r.Context(), baseURL, apiKey, model); err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		if err := s.persistModelConfig(baseURL, apiKey, model); err != nil {
			http.Error(w, "unable to save model config", http.StatusInternalServerError)
			return
		}
		s.setModelConfig(baseURL, apiKey, model)
		writeJSON(w, http.StatusOK, s.modelConfigResponse())
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
