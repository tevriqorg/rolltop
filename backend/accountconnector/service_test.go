package accountconnector

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	secretcrypto "rolltop/backend/crypto"
	"rolltop/backend/store"
)

type fakeConnector struct {
	grant       Grant
	refreshed   TokenSet
	seenRefresh string
}

func (f *fakeConnector) ID() string { return "google" }
func (f *fakeConnector) Scopes([]Capability) ([]string, error) {
	return []string{"openid", "email", "gmail.modify"}, nil
}
func (f *fakeConnector) AuthorizationURL(AuthRequest) (string, error) { return "", nil }
func (f *fakeConnector) Exchange(context.Context, string, PKCE) (Grant, error) {
	return f.grant, nil
}
func (f *fakeConnector) Refresh(_ context.Context, refreshToken string) (TokenSet, error) {
	f.seenRefresh = refreshToken
	return f.refreshed, nil
}
func testService(t *testing.T) (Service, store.User) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	user, err := st.CreateUser(context.Background(), "owner@example.test", "Owner", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("0123456789abcdef0123456789abcdef")
	return Service{Store: st, MasterKey: key}, user
}

func TestServicePersistsOnlyEncryptedRefreshToken(t *testing.T) {
	svc, user := testService(t)
	connector := &fakeConnector{grant: Grant{
		Identity: IdentityInfo{Subject: "google-123", Email: "owner@gmail.test", EmailVerified: true, Name: "Owner"},
		Tokens:   TokenSet{AccessToken: "access-1", RefreshToken: "refresh-secret", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)},
	}}
	connection, tokens, err := svc.FinishAuthorization(context.Background(), user.ID, connector, []Capability{MailModify, MailSend}, "code", PKCE{})
	if err != nil {
		t.Fatal(err)
	}
	if tokens.RefreshToken != "" || tokens.AccessToken != "access-1" {
		t.Fatalf("returned tokens=%+v", tokens)
	}
	if connection.EncryptedRefreshToken == "" || connection.EncryptedRefreshToken == "refresh-secret" {
		t.Fatalf("refresh token was not encrypted")
	}
	plain, err := secretcrypto.DecryptString(svc.MasterKey, connection.EncryptedRefreshToken)
	if err != nil || plain != "refresh-secret" {
		t.Fatalf("decrypt=%q err=%v", plain, err)
	}
	if len(connection.Capabilities) != 2 {
		t.Fatalf("capabilities=%v", connection.Capabilities)
	}
}
func TestServiceRefreshesWithoutExposingRefreshToken(t *testing.T) {
	svc, user := testService(t)
	connector := &fakeConnector{
		grant: Grant{
			Identity: IdentityInfo{Subject: "google-123", Email: "owner@gmail.test", EmailVerified: true},
			Tokens:   TokenSet{AccessToken: "access-1", RefreshToken: "refresh-1", Scopes: []string{"openid", "email", "gmail.modify"}},
		},
		refreshed: TokenSet{AccessToken: "access-2", RefreshToken: "refresh-2", TokenType: "Bearer", Expiry: time.Now().Add(time.Hour)},
	}
	connection, _, err := svc.FinishAuthorization(context.Background(), user.ID, connector, []Capability{MailModify}, "code", PKCE{})
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := svc.RefreshAccess(context.Background(), user.ID, connector, connection.ExternalSubject)
	if err != nil {
		t.Fatal(err)
	}
	if connector.seenRefresh != "refresh-1" {
		t.Fatalf("refresh input=%q", connector.seenRefresh)
	}
	if tokens.AccessToken != "access-2" || tokens.RefreshToken != "" {
		t.Fatalf("returned tokens=%+v", tokens)
	}
	stored, err := svc.Store.GetAccountConnection(context.Background(), user.ID, "google", "google-123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := secretcrypto.DecryptString(svc.MasterKey, stored.EncryptedRefreshToken)
	if err != nil || plain != "refresh-2" {
		t.Fatalf("rotated refresh=%q err=%v", plain, err)
	}
	if len(stored.Scopes) != 3 {
		t.Fatalf("preserved scopes=%v", stored.Scopes)
	}
}

func TestServiceRejectsUnverifiedIdentity(t *testing.T) {
	svc, user := testService(t)
	connector := &fakeConnector{grant: Grant{Identity: IdentityInfo{Subject: "google-123", Email: "owner@gmail.test"}, Tokens: TokenSet{RefreshToken: "refresh"}}}
	if _, _, err := svc.FinishAuthorization(context.Background(), user.ID, connector, []Capability{MailRead}, "code", PKCE{}); err == nil {
		t.Fatal("unverified identity was accepted")
	}
}
