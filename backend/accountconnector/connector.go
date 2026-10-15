// Package accountconnector defines reusable server-side OAuth account connectors.
package accountconnector

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
)

type Capability string

const (
	Identity   Capability = "identity"
	MailRead   Capability = "mail.read"
	MailModify Capability = "mail.modify"
	MailSend   Capability = "mail.send"
)

type PKCE struct {
	Verifier  string
	Challenge string
}

func NewPKCE() (PKCE, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return PKCE{}, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{Verifier: verifier, Challenge: base64.RawURLEncoding.EncodeToString(sum[:])}, nil
}

type AuthRequest struct {
	State        string
	PKCE         PKCE
	Capabilities []Capability
	ForceConsent bool
}

type IdentityInfo struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
}

type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	Scopes       []string
	Expiry       time.Time
}

type Grant struct {
	Identity IdentityInfo
	Tokens   TokenSet
}

type Connector interface {
	ID() string
	Scopes([]Capability) ([]string, error)
	AuthorizationURL(AuthRequest) (string, error)
	Exchange(context.Context, string, PKCE) (Grant, error)
	Refresh(context.Context, string) (TokenSet, error)
}
