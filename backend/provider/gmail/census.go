// Package gmail implements Gmail REST-specific provider primitives.
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"rolltop/backend/provider"
)

const defaultBaseURL = "https://gmail.googleapis.com/gmail/v1"

type Client struct {
	BaseURL     string
	AccessToken string
	HTTPClient  *http.Client
}

func (c Client) Census(ctx context.Context, account provider.Account) (provider.Census, error) {
	if account.UserID <= 0 || account.ID == "" {
		return provider.Census{}, errors.New("gmail census requires tenant and account")
	}
	if strings.TrimSpace(c.AccessToken) == "" {
		return provider.Census{}, errors.New("gmail census requires access token")
	}
	ids, err := c.listMessageIDs(ctx)
	if err != nil {
		return provider.Census{}, err
	}
	bytes, err := c.sumSizeEstimates(ctx, ids)
	if err != nil {
		return provider.Census{}, err
	}
	return provider.Census{Account: account, MessageCount: len(ids), EstimatedRawBytes: bytes,
		ExpectedEvidenceBytes: bytes, RecommendedFreeDisk: bytes + (bytes+1)/2,
		Method: "Gmail messages.list + messages.get(sizeEstimate); metadata only", Exact: false}, nil
}
func (c Client) httpClient() *http.Client {
	client := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if c.HTTPClient != nil {
		client = *c.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 20 * time.Second
		}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return &client
}

func (c Client) baseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); value != "" {
		return value
	}
	return defaultBaseURL
}

func (c Client) getJSON(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.AccessToken)
	res, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("gmail API %s returned HTTP %d", path, res.StatusCode)
	}
	return json.NewDecoder(res.Body).Decode(out)
}
func (c Client) listMessageIDs(ctx context.Context) ([]string, error) {
	ids := []string{}
	pageToken := ""
	for {
		q := url.Values{"maxResults": {"500"}, "includeSpamTrash": {"true"}, "fields": {"messages/id,nextPageToken"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var page struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := c.getJSON(ctx, "/users/me/messages?"+q.Encode(), &page); err != nil {
			return nil, err
		}
		for _, message := range page.Messages {
			if strings.TrimSpace(message.ID) != "" {
				ids = append(ids, message.ID)
			}
		}
		if page.NextPageToken == "" {
			return ids, nil
		}
		pageToken = page.NextPageToken
	}
}

func (c Client) messageSize(ctx context.Context, id string) (int64, error) {
	q := url.Values{"format": {"metadata"}, "fields": {"id,sizeEstimate"}}
	var message struct {
		ID           string `json:"id"`
		SizeEstimate int64  `json:"sizeEstimate"`
	}
	if err := c.getJSON(ctx, "/users/me/messages/"+url.PathEscape(id)+"?"+q.Encode(), &message); err != nil {
		return 0, err
	}
	if message.SizeEstimate < 0 {
		return 0, errors.New("gmail returned a negative size estimate")
	}
	return message.SizeEstimate, nil
}
func (c Client) sumSizeEstimates(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan string)
	var mu sync.Mutex
	var total int64
	var firstErr error
	var wg sync.WaitGroup
	workers := 8
	if len(ids) < workers {
		workers = len(ids)
	}
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				size, err := c.messageSize(ctx, id)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
					cancel()
				}
				if err == nil {
					total += size
				}
				mu.Unlock()
				if err != nil {
					return
				}
			}
		}()
	}
	for _, id := range ids {
		select {
		case <-ctx.Done():
			break
		case jobs <- id:
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(jobs)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if firstErr != nil {
		return 0, firstErr
	}
	return total, nil
}
