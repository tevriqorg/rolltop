package accountconnector

import (
	"context"
	"errors"
	"strings"

	secretcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
)

// Service owns the server-side token lifecycle. Refresh tokens never need to
// leave this boundary after an authorization code is exchanged.
type Service struct {
	Store     *store.Store
	MasterKey []byte
}

func (s Service) FinishAuthorization(ctx context.Context, userID int64, connector Connector, capabilities []Capability, code string, pkce PKCE) (store.AccountConnection, TokenSet, error) {
	if err := s.validate(connector, userID); err != nil {
		return store.AccountConnection{}, TokenSet{}, err
	}
	grant, err := connector.Exchange(ctx, code, pkce)
	if err != nil {
		return store.AccountConnection{}, TokenSet{}, err
	}
	connection, err := s.persistGrant(ctx, userID, connector, capabilities, grant)
	if err != nil {
		return store.AccountConnection{}, TokenSet{}, err
	}
	return connection, withoutRefreshToken(grant.Tokens), nil
}
func (s Service) RefreshAccess(ctx context.Context, userID int64, connector Connector, externalSubject string) (TokenSet, error) {
	if err := s.validate(connector, userID); err != nil {
		return TokenSet{}, err
	}
	connection, err := s.Store.GetAccountConnection(ctx, userID, connector.ID(), externalSubject)
	if err != nil {
		return TokenSet{}, err
	}
	refreshToken, err := secretcrypto.DecryptString(s.MasterKey, connection.EncryptedRefreshToken)
	if err != nil {
		return TokenSet{}, err
	}
	tokens, err := connector.Refresh(ctx, refreshToken)
	if err != nil {
		return TokenSet{}, err
	}
	persistedRefresh := strings.TrimSpace(tokens.RefreshToken)
	if persistedRefresh == "" {
		persistedRefresh = refreshToken
	}
	if len(tokens.Scopes) == 0 {
		tokens.Scopes = append([]string(nil), connection.Scopes...)
	}
	if err := s.updateTokens(ctx, connection, persistedRefresh, tokens.Scopes); err != nil {
		return TokenSet{}, err
	}
	return withoutRefreshToken(tokens), nil
}

func (s Service) validate(connector Connector, userID int64) error {
	if s.Store == nil || connector == nil || userID <= 0 {
		return errors.New("account connector service requires store, connector, and tenant")
	}
	if len(s.MasterKey) != 32 {
		return errors.New("account connector service requires a 32-byte master key")
	}
	return nil
}
func (s Service) persistGrant(ctx context.Context, userID int64, connector Connector, capabilities []Capability, grant Grant) (store.AccountConnection, error) {
	if !grant.Identity.EmailVerified || strings.TrimSpace(grant.Identity.Subject) == "" || strings.TrimSpace(grant.Identity.Email) == "" {
		return store.AccountConnection{}, errors.New("account connector requires a verified identity")
	}
	if strings.TrimSpace(grant.Tokens.RefreshToken) == "" {
		return store.AccountConnection{}, errors.New("account connector authorization returned no refresh token")
	}
	scopes := append([]string(nil), grant.Tokens.Scopes...)
	if len(scopes) == 0 {
		var err error
		scopes, err = connector.Scopes(capabilities)
		if err != nil {
			return store.AccountConnection{}, err
		}
	}
	encrypted, err := secretcrypto.EncryptString(s.MasterKey, grant.Tokens.RefreshToken)
	if err != nil {
		return store.AccountConnection{}, err
	}
	return s.Store.UpsertAccountConnection(ctx, userID, store.AccountConnection{
		Provider: connector.ID(), ExternalSubject: grant.Identity.Subject, Email: grant.Identity.Email,
		DisplayName: grant.Identity.Name, EncryptedRefreshToken: encrypted, Scopes: scopes,
		Capabilities: capabilityStrings(capabilities),
	})
}

func (s Service) updateTokens(ctx context.Context, connection store.AccountConnection, refreshToken string, scopes []string) error {
	encrypted, err := secretcrypto.EncryptString(s.MasterKey, refreshToken)
	if err != nil {
		return err
	}
	connection.EncryptedRefreshToken = encrypted
	connection.Scopes = append([]string(nil), scopes...)
	_, err = s.Store.UpsertAccountConnection(ctx, connection.UserID, connection)
	return err
}
func capabilityStrings(capabilities []Capability) []string {
	out := make([]string, 0, len(capabilities))
	seen := map[string]bool{}
	for _, capability := range capabilities {
		value := strings.TrimSpace(string(capability))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func withoutRefreshToken(tokens TokenSet) TokenSet {
	tokens.RefreshToken = ""
	return tokens
}
