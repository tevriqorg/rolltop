package google

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"rolltop/backend/accountconnector"
)

const (
	defaultAuthorizationEndpoint = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultTokenEndpoint         = "https://oauth2.googleapis.com/token"
	defaultUserinfoEndpoint      = "https://openidconnect.googleapis.com/v1/userinfo"
	gmailReadonlyScope           = "https://www.googleapis.com/auth/gmail.readonly"
	gmailModifyScope             = "https://www.googleapis.com/auth/gmail.modify"
	gmailSendScope               = "https://www.googleapis.com/auth/gmail.send"
)

type Config struct {
	ClientID, ClientSecret, RedirectURL                    string
	AuthorizationEndpoint, TokenEndpoint, UserinfoEndpoint string
	HTTPClient                                             *http.Client
	Now                                                    func() time.Time
}
type Connector struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Connector, error) {
	if strings.TrimSpace(cfg.ClientID) == "" || strings.TrimSpace(cfg.ClientSecret) == "" {
		return nil, errors.New("google oauth client credentials are required")
	}
	if err := validateEndpoint("redirect URL", cfg.RedirectURL, true); err != nil {
		return nil, err
	}
	cfg.AuthorizationEndpoint = first(cfg.AuthorizationEndpoint, defaultAuthorizationEndpoint)
	cfg.TokenEndpoint = first(cfg.TokenEndpoint, defaultTokenEndpoint)
	cfg.UserinfoEndpoint = first(cfg.UserinfoEndpoint, defaultUserinfoEndpoint)
	for label, endpoint := range map[string]string{
		"authorization endpoint": cfg.AuthorizationEndpoint,
		"token endpoint":         cfg.TokenEndpoint,
		"userinfo endpoint":      cfg.UserinfoEndpoint,
	} {
		if err := validateEndpoint(label, endpoint, false); err != nil {
			return nil, err
		}
	}
	client := http.Client{Timeout: 15 * time.Second, CheckRedirect: refuseRedirect}
	if cfg.HTTPClient != nil {
		client = *cfg.HTTPClient
		if client.Timeout == 0 {
			client.Timeout = 15 * time.Second
		}
		client.CheckRedirect = refuseRedirect
	}
	return &Connector{cfg: cfg, client: &client}, nil
}

func (c *Connector) ID() string { return "google" }

func (c *Connector) Scopes(capabilities []accountconnector.Capability) ([]string, error) {
	scopes := []string{"openid", "email"}
	extra := map[string]bool{}
	modify := false
	for _, capability := range capabilities {
		switch capability {
		case accountconnector.Identity:
		case accountconnector.MailRead:
			extra[gmailReadonlyScope] = true
		case accountconnector.MailModify:
			modify = true
		case accountconnector.MailSend:
			extra[gmailSendScope] = true
		default:
			return nil, fmt.Errorf("unsupported google capability %q", capability)
		}
	}
	if modify {
		delete(extra, gmailReadonlyScope)
		delete(extra, gmailSendScope)
		extra[gmailModifyScope] = true
	}
	keys := make([]string, 0, len(extra))
	for scope := range extra {
		keys = append(keys, scope)
	}
	sort.Strings(keys)
	return append(scopes, keys...), nil
}

func (c *Connector) AuthorizationURL(in accountconnector.AuthRequest) (string, error) {
	if strings.TrimSpace(in.State) == "" {
		return "", errors.New("oauth state is required")
	}
	if in.PKCE.Verifier == "" || in.PKCE.Challenge == "" {
		return "", errors.New("pkce verifier and challenge are required")
	}
	scopes, err := c.Scopes(in.Capabilities)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(c.cfg.AuthorizationEndpoint)
	if err != nil {
		return "", err
	}
	q := u.Query()
	q.Set("client_id", c.cfg.ClientID)
	q.Set("redirect_uri", c.cfg.RedirectURL)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("state", in.State)
	q.Set("code_challenge", in.PKCE.Challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline")
	q.Set("include_granted_scopes", "true")
	if in.ForceConsent {
		q.Set("prompt", "consent")
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
}

type userinfoResponse struct {
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
}

func (c *Connector) Exchange(ctx context.Context, code string, pkce accountconnector.PKCE) (accountconnector.Grant, error) {
	if strings.TrimSpace(code) == "" || pkce.Verifier == "" {
		return accountconnector.Grant{}, errors.New("authorization code and pkce verifier are required")
	}
	values := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {c.cfg.RedirectURL},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
		"code_verifier": {pkce.Verifier},
	}
	tokens, err := c.exchangeToken(ctx, values, true)
	if err != nil {
		return accountconnector.Grant{}, err
	}
	identity, err := c.userinfo(ctx, tokens.AccessToken)
	if err != nil {
		return accountconnector.Grant{}, err
	}
	return accountconnector.Grant{Identity: identity, Tokens: tokens}, nil
}

func (c *Connector) Refresh(ctx context.Context, refreshToken string) (accountconnector.TokenSet, error) {
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return accountconnector.TokenSet{}, errors.New("refresh token is required")
	}
	values := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {c.cfg.ClientID},
		"client_secret": {c.cfg.ClientSecret},
	}
	tokens, err := c.exchangeToken(ctx, values, false)
	if err != nil {
		return accountconnector.TokenSet{}, err
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = refreshToken
	}
	return tokens, nil
}

func (c *Connector) exchangeToken(ctx context.Context, values url.Values, requireRefresh bool) (accountconnector.TokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.TokenEndpoint, strings.NewReader(values.Encode()))
	if err != nil {
		return accountconnector.TokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.client.Do(req)
	if err != nil {
		return accountconnector.TokenSet{}, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return accountconnector.TokenSet{}, err
	}
	if res.StatusCode/100 != 2 {
		return accountconnector.TokenSet{}, fmt.Errorf("google token exchange failed with status %d", res.StatusCode)
	}
	var out tokenResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return accountconnector.TokenSet{}, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return accountconnector.TokenSet{}, errors.New("google token response did not include an access token")
	}
	if requireRefresh && strings.TrimSpace(out.RefreshToken) == "" {
		return accountconnector.TokenSet{}, errors.New("google initial authorization did not include a refresh token")
	}
	now := time.Now
	if c.cfg.Now != nil {
		now = c.cfg.Now
	}
	expiry := time.Time{}
	if out.ExpiresIn > 0 {
		expiry = now().UTC().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return accountconnector.TokenSet{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		TokenType:    out.TokenType,
		Scopes:       strings.Fields(out.Scope),
		Expiry:       expiry,
	}, nil
}

func (c *Connector) userinfo(ctx context.Context, accessToken string) (accountconnector.IdentityInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.cfg.UserinfoEndpoint, nil)
	if err != nil {
		return accountconnector.IdentityInfo{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	res, err := c.client.Do(req)
	if err != nil {
		return accountconnector.IdentityInfo{}, err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return accountconnector.IdentityInfo{}, fmt.Errorf("google userinfo failed with status %d", res.StatusCode)
	}
	var out userinfoResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
		return accountconnector.IdentityInfo{}, err
	}
	email := strings.ToLower(strings.TrimSpace(out.Email))
	if strings.TrimSpace(out.Subject) == "" || email == "" || !strings.Contains(email, "@") {
		return accountconnector.IdentityInfo{}, errors.New("google userinfo is missing a usable identity")
	}
	if !out.EmailVerified {
		return accountconnector.IdentityInfo{}, errors.New("google account email is not verified")
	}
	return accountconnector.IdentityInfo{Subject: out.Subject, Email: email, EmailVerified: true, Name: strings.TrimSpace(out.Name)}, nil
}
func validateEndpoint(label, raw string, allowRedirect bool) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return fmt.Errorf("google %s must be an absolute URL", label)
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return fmt.Errorf("google %s must use https", label)
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	if allowRedirect {
		return errors.New("google redirect URL may use http only on loopback")
	}
	return fmt.Errorf("google %s may use http only on loopback", label)
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return errors.New("google oauth redirect refused")
}

func first(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}
