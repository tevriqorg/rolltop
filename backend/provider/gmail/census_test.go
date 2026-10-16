package gmail

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"rolltop/backend/provider"
)

func TestCensusUsesMetadataOnlyAndSumsSizeEstimate(t *testing.T) {
	var mu sync.Mutex
	gets := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access-token" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.URL.Path == "/users/me/messages" {
			if r.URL.Query().Get("includeSpamTrash") != "true" || r.URL.Query().Get("maxResults") != "500" {
				t.Errorf("list query=%s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("pageToken") == "" {
				_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "m1"}, {"id": "m2"}}, "nextPageToken": "page-2"})
			} else {
				_ = json.NewEncoder(w).Encode(map[string]any{"messages": []map[string]string{{"id": "m3"}}})
			}
			return
		}
		if strings.HasPrefix(r.URL.Path, "/users/me/messages/") {
			if r.URL.Query().Get("format") != "metadata" || r.URL.Query().Get("fields") != "id,sizeEstimate" {
				t.Errorf("message query=%s", r.URL.RawQuery)
			}
			id := strings.TrimPrefix(r.URL.Path, "/users/me/messages/")
			mu.Lock()
			gets[id]++
			mu.Unlock()
			sizes := map[string]int64{"m1": 100, "m2": 250, "m3": 650}
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id, "sizeEstimate": sizes[id]})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	client := Client{BaseURL: srv.URL, AccessToken: "access-token", HTTPClient: srv.Client()}
	census, err := client.Census(context.Background(), provider.Account{UserID: 7, ID: "primary", Provider: provider.Kind("gmail")})
	if err != nil {
		t.Fatal(err)
	}
	if census.MessageCount != 3 || census.EstimatedRawBytes != 1000 || census.ExpectedEvidenceBytes != 1000 || census.RecommendedFreeDisk != 1500 {
		t.Fatalf("census=%+v", census)
	}
	if census.Exact || !strings.Contains(census.Method, "metadata only") {
		t.Fatalf("census metadata=%+v", census)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, id := range []string{"m1", "m2", "m3"} {
		if gets[id] != 1 {
			t.Fatalf("%s gets=%d", id, gets[id])
		}
	}
}

func TestCensusRefusesRedirects(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Fatal("redirect target reached") }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer source.Close()
	_, err := (Client{BaseURL: source.URL, AccessToken: "token", HTTPClient: source.Client()}).Census(context.Background(), provider.Account{UserID: 1, ID: "primary", Provider: provider.Kind("gmail")})
	if err == nil {
		t.Fatal("redirect was accepted")
	}
}
