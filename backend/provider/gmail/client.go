package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://gmail.googleapis.com/gmail/v1"

var ErrUnauthorized = errors.New("gmail access token is unauthorized")

type HTTPError struct {
	StatusCode int
	Path       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("gmail API %s returned HTTP %d", e.Path, e.StatusCode)
}

type Client struct {
	BaseURL            string
	AccessToken        string
	HTTPClient         *http.Client
	MaxRawMessageBytes int64
	MaxAttempts        int
	InitialBackoff     time.Duration
	Sleep              func(context.Context, time.Duration) error
}

func (c Client) baseURL() string {
	if value := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/"); value != "" {
		return value
	}
	return defaultBaseURL
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
func (c Client) attempts() int {
	if c.MaxAttempts > 0 {
		return c.MaxAttempts
	}
	return 4
}
func (c Client) backoff() time.Duration {
	if c.InitialBackoff > 0 {
		return c.InitialBackoff
	}
	return 250 * time.Millisecond
}
func (c Client) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == 500 || code == 502 || code == 503 || code == 504
}
func retryDelay(header http.Header, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(header.Get("Retry-After"))
	if v == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(v); err == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}
	return fallback
}

func (c Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	if strings.TrimSpace(c.AccessToken) == "" {
		return errors.New("gmail API requires access token")
	}
	var payload []byte
	var err error
	if in != nil {
		payload, err = json.Marshal(in)
		if err != nil {
			return err
		}
	}
	backoff := c.backoff()
	for attempt := 1; attempt <= c.attempts(); attempt++ {
		var body io.Reader
		if payload != nil {
			body = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, body)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.AccessToken)
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.httpClient().Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if attempt == c.attempts() {
				return err
			}
			if err := c.sleep(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		}
		if res.StatusCode/100 == 2 {
			defer res.Body.Close()
			if out == nil {
				_, _ = io.Copy(io.Discard, res.Body)
				return nil
			}
			return json.NewDecoder(io.LimitReader(res.Body, 32<<20)).Decode(out)
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		_ = res.Body.Close()
		if res.StatusCode == http.StatusUnauthorized {
			return ErrUnauthorized
		}
		if !retryableStatus(res.StatusCode) || attempt == c.attempts() {
			return &HTTPError{StatusCode: res.StatusCode, Path: path}
		}
		delay := retryDelay(res.Header, backoff)
		if err := c.sleep(ctx, delay); err != nil {
			return err
		}
		backoff *= 2
	}
	return errors.New("gmail API retry loop exhausted")
}

func (c Client) getJSON(ctx context.Context, path string, out any) error {
	return c.doJSON(ctx, http.MethodGet, path, nil, out)
}
func (c Client) postJSON(ctx context.Context, path string, in, out any) error {
	return c.doJSON(ctx, http.MethodPost, path, in, out)
}
