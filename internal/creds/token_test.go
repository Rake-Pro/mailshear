package creds

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/oauth"
)

func oauthAccount() config.Account {
	return config.Account{
		Name: "personal", Host: "imap.gmail.com", Port: 993,
		Username: "me@gmail.com", Auth: "oauth",
		OAuth: &config.OAuth{Provider: "google", ClientID: "cid", ClientSecret: "sec"},
	}
}

func TestSaveLoadDeleteToken(t *testing.T) {
	dir := t.TempDir()
	a := oauthAccount()

	if _, err := LoadToken(dir, a); !errors.Is(err, ErrNoToken) {
		t.Fatalf("LoadToken on a fresh dir = %v, want ErrNoToken", err)
	}
	if HasToken(dir, a) {
		t.Fatal("HasToken reported a token that was never written")
	}

	want := oauth.Token{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		TokenType:    "Bearer",
		Expiry:       time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC),
		Scope:        "https://mail.google.com/",
	}
	if err := SaveToken(dir, a, want); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}
	if !HasToken(dir, a) {
		t.Fatal("HasToken did not see the written token")
	}

	path := filepath.Join(dir, "credentials", "personal.oauth.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", info.Mode().Perm())
	}

	got, err := LoadToken(dir, a)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken || !got.Expiry.Equal(want.Expiry) {
		t.Fatalf("token = %+v, want %+v", got, want)
	}

	if err := DeleteToken(dir, a); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}
	if err := DeleteToken(dir, a); !errors.Is(err, ErrNoToken) {
		t.Fatalf("second DeleteToken = %v, want ErrNoToken", err)
	}
}

func TestAccessTokenUsesTheStoredOneWhileValid(t *testing.T) {
	dir := t.TempDir()
	a := oauthAccount()
	if err := SaveToken(dir, a, oauth.Token{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	// No token endpoint configured: a refresh would fail the test outright.
	cl := &oauth.Client{ClientID: "cid", Spec: oauth.ProviderSpec{Name: "fake"}}
	got, err := AccessToken(context.Background(), dir, a, cl)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "at-1" {
		t.Fatalf("access token = %q", got)
	}
}

func TestAccessTokenRefreshesAndStores(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"at-2","expires_in":3600}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := oauthAccount()
	if err := SaveToken(dir, a, oauth.Token{AccessToken: "at-1", RefreshToken: "rt-1", Expiry: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	cl := &oauth.Client{ClientID: "cid", Spec: oauth.ProviderSpec{Name: "fake", TokenURL: srv.URL}}
	got, err := AccessToken(context.Background(), dir, a, cl)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if got != "at-2" {
		t.Fatalf("access token = %q, want the refreshed one", got)
	}

	stored, err := LoadToken(dir, a)
	if err != nil {
		t.Fatalf("LoadToken: %v", err)
	}
	if stored.AccessToken != "at-2" || stored.RefreshToken != "rt-1" {
		t.Fatalf("stored token = %+v, want the new access token and the old refresh token", stored)
	}
}

func TestAccessTokenReauthMessagePointsAtTheAccountsScreen(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"revoked"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := oauthAccount()
	if err := SaveToken(dir, a, oauth.Token{AccessToken: "at-1", RefreshToken: "rt-1"}); err != nil {
		t.Fatalf("SaveToken: %v", err)
	}

	cl := &oauth.Client{ClientID: "cid", Spec: oauth.ProviderSpec{Name: "fake", TokenURL: srv.URL}}
	_, err := AccessToken(context.Background(), dir, a, cl)
	if err == nil {
		t.Fatal("AccessToken accepted a revoked refresh token")
	}
	if want := "sign in again from the accounts screen"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to name %q", err, want)
	}
}

func TestResolveRefusesOAuthAccounts(t *testing.T) {
	if _, err := Resolve(t.TempDir(), oauthAccount()); err == nil {
		t.Fatal("Resolve returned a password for an OAuth account")
	}
}

func TestOAuthClientFromAccount(t *testing.T) {
	cl, err := OAuthClient(oauthAccount())
	if err != nil {
		t.Fatalf("OAuthClient: %v", err)
	}
	if cl.ClientID != "cid" || cl.ClientSecret != "sec" || cl.Spec.Name != "google" {
		t.Fatalf("client = %+v", cl)
	}
	if _, err := OAuthClient(config.Account{Name: "x", Auth: "oauth"}); err == nil {
		t.Fatal("OAuthClient accepted an account with no oauth block")
	}
}
