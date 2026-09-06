package creds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/oauth"
)

// tokenPath is where an account's OAuth token lives: beside the password
// file, in the same 0700 directory, at mode 0600.
func tokenPath(dataDir string, a config.Account) string {
	return filepath.Join(dataDir, "credentials", a.Name+".oauth.json")
}

// ErrNoToken reports that the account has never signed in, or has been
// signed out.
var ErrNoToken = errors.New("no OAuth token stored")

// LoadToken reads the stored token for an account.
func LoadToken(dataDir string, a config.Account) (oauth.Token, error) {
	path := tokenPath(dataDir, a)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return oauth.Token{}, fmt.Errorf("account %s: %w; sign in from the accounts screen", a.Name, ErrNoToken)
		}
		return oauth.Token{}, fmt.Errorf("account %s: reading %s: %w", a.Name, path, err)
	}
	var t oauth.Token
	if err := json.Unmarshal(data, &t); err != nil {
		return oauth.Token{}, fmt.Errorf("account %s: parsing %s: %w; sign in again from the accounts screen", a.Name, path, err)
	}
	return t, nil
}

// SaveToken writes the token atomically at mode 0600.
func SaveToken(dataDir string, a config.Account, t oauth.Token) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding the token for %s: %w", a.Name, err)
	}
	return writeSecret(dataDir, a.Name+".oauth", append(data, '\n'), tokenPath(dataDir, a))
}

// DeleteToken removes the stored token, signing the account out locally. The
// token is not revoked at the provider; that is done from the provider's own
// account page.
func DeleteToken(dataDir string, a config.Account) error {
	path := tokenPath(dataDir, a)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("account %s: %w", a.Name, ErrNoToken)
		}
		return fmt.Errorf("account %s: deleting %s: %w", a.Name, path, err)
	}
	return nil
}

// HasToken reports whether a token file exists for the account.
func HasToken(dataDir string, a config.Account) bool {
	_, err := os.Stat(tokenPath(dataDir, a))
	return err == nil
}

// OAuthClient builds the OAuth client for an account from its config block.
// The caller sets OpenURL when it can reach a browser.
func OAuthClient(a config.Account) (*oauth.Client, error) {
	spec, err := a.OAuthSpec()
	if err != nil {
		return nil, fmt.Errorf("account %s: %w", a.Name, err)
	}
	return &oauth.Client{
		ClientID:     a.OAuth.ClientID,
		ClientSecret: a.OAuth.ClientSecret,
		Spec:         spec,
	}, nil
}

// AccessToken returns a usable access token for an account: the stored one
// while it is still valid, otherwise a refreshed one, which is written back
// before it is returned. A refresh the provider refuses outright means the
// user has to sign in again, and the error says so.
func AccessToken(ctx context.Context, dataDir string, a config.Account, cl *oauth.Client) (string, error) {
	if cl == nil {
		var err error
		if cl, err = OAuthClient(a); err != nil {
			return "", err
		}
	}
	t, err := LoadToken(dataDir, a)
	if err != nil {
		return "", err
	}
	if t.Valid(time.Now()) {
		return t.AccessToken, nil
	}

	next, err := cl.Refresh(ctx, t)
	if err != nil {
		if errors.Is(err, oauth.ErrReauth) {
			return "", fmt.Errorf("account %s: the stored OAuth token is no longer valid (%v); sign in again from the accounts screen", a.Name, err)
		}
		return "", fmt.Errorf("account %s: refreshing the OAuth token: %w", a.Name, err)
	}
	if err := SaveToken(dataDir, a, next); err != nil {
		return "", err
	}
	return next.AccessToken, nil
}
