package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	mmcrypto "rolltop/backend/crypto"
)

func TestAccountConnectionStoresOnlyEncryptedRefreshToken(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	user, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	key := []byte("12345678901234567890123456789012")
	plain := "google-refresh-secret"
	encrypted, err := mmcrypto.EncryptString(key, plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := st.UpsertAccountConnection(ctx, user.ID, AccountConnection{
		Provider: "google", ExternalSubject: "subject-1", Email: "Owner@Example.test",
		DisplayName: "Owner", EncryptedRefreshToken: encrypted,
		Scopes: []string{"scope-b", "scope-a", "scope-a"}, Capabilities: []string{"mail.read", "identity"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != "owner@example.test" || got.EncryptedRefreshToken == plain || !strings.HasPrefix(got.EncryptedRefreshToken, "v1:") {
		t.Fatalf("stored connection=%+v", got)
	}
	if strings.Join(got.Scopes, "|") != "scope-a|scope-b" || strings.Join(got.Capabilities, "|") != "identity|mail.read" {
		t.Fatalf("normalized metadata scopes=%v capabilities=%v", got.Scopes, got.Capabilities)
	}
	var stored string
	if err := st.DB().QueryRowContext(ctx, `SELECT encrypted_refresh_token FROM account_connections WHERE id=?`, got.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == plain || strings.Contains(stored, plain) {
		t.Fatal("refresh token was stored in plaintext")
	}
	decoded, err := mmcrypto.DecryptString(key, stored)
	if err != nil || decoded != plain {
		t.Fatalf("decrypt stored refresh token=%q err=%v", decoded, err)
	}

	rotated, err := mmcrypto.EncryptString(key, "rotated-refresh")
	if err != nil {
		t.Fatal(err)
	}
	updated, err := st.UpsertAccountConnection(ctx, user.ID, AccountConnection{
		Provider: "google", ExternalSubject: "subject-1", Email: "owner@example.test",
		DisplayName: "Updated", EncryptedRefreshToken: rotated,
		Scopes: []string{"scope-c"}, Capabilities: []string{"mail.modify"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != got.ID || updated.DisplayName != "Updated" || strings.Join(updated.Scopes, "|") != "scope-c" {
		t.Fatalf("updated connection=%+v", updated)
	}
}
func TestAccountConnectionRejectsPlaintextAndIsTenantScoped(t *testing.T) {
	ctx := context.Background()
	st, err := Open(filepath.Join(t.TempDir(), "rolltop.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	owner, err := st.CreateUser(ctx, "owner@example.test", "Owner", "hash", true)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateUser(ctx, "other@example.test", "Other", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertAccountConnection(ctx, owner.ID, AccountConnection{
		Provider: "google", ExternalSubject: "subject-1", Email: "owner@example.test",
		EncryptedRefreshToken: "plaintext-refresh-token",
	})
	if err == nil || !strings.Contains(err.Error(), "must be encrypted") {
		t.Fatalf("plaintext refresh token err=%v", err)
	}
	key := []byte("12345678901234567890123456789012")
	encrypted, err := mmcrypto.EncryptString(key, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.UpsertAccountConnection(ctx, owner.ID, AccountConnection{
		Provider: "google", ExternalSubject: "subject-1", Email: "owner@example.test",
		EncryptedRefreshToken: encrypted,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.GetAccountConnection(ctx, other.ID, "google", "subject-1")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-tenant lookup err=%v, want not found", err)
	}
}
