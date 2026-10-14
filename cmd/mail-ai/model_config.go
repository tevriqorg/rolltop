package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
		_, currentKey, _ := s.currentModelConfig()
		apiKey := currentKey
		if in.APIKey != nil {
			apiKey = strings.TrimSpace(*in.APIKey)
		} else if in.ClearAPIKey {
			apiKey = ""
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
