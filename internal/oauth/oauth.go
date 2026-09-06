// Package oauth implements the OAuth 2.0 authorization-code flow with PKCE
// against a loopback redirect, plus the token refresh and the SASL/SMTP
// XOAUTH2 plumbing that IMAP and SMTP need.
//
// It is standard library only on purpose: the client is a few hundred lines
// of net/http and crypto/rand, and the alternative pulls a dependency in for
// one grant type. Client IDs are supplied by the user, never shipped here.
package oauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Rake-Pro/mailshear/internal/headers"
)

// ProviderSpec is one identity provider's endpoints and the scopes a mail
// client has to ask for. DeviceAuthURL is recorded for providers that offer
// the device flow; this build does not use it.
type ProviderSpec struct {
	Name          string
	AuthURL       string
	TokenURL      string
	Scopes        []string
	DeviceAuthURL string
}

// Google is the spec for Google accounts, personal and Workspace. The single
// https://mail.google.com/ scope covers IMAP and SMTP; Google has no finer
// grained mail scope that IMAP accepts.
func Google() ProviderSpec {
	return ProviderSpec{
		Name:          "google",
		AuthURL:       "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL:      "https://oauth2.googleapis.com/token",
		Scopes:        []string{"https://mail.google.com/"},
		DeviceAuthURL: "https://oauth2.googleapis.com/device/code",
	}
}

// Microsoft is the spec for Outlook, Hotmail and Microsoft 365. tenant is the
// Entra tenant id, or "common" for an app registration that accepts both
// personal and organizational accounts; an empty tenant means "common".
func Microsoft(tenant string) ProviderSpec {
	tenant = strings.TrimSpace(tenant)
	if tenant == "" {
		tenant = "common"
	}
	base := "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0"
	return ProviderSpec{
		Name:     "microsoft",
		AuthURL:  base + "/authorize",
		TokenURL: base + "/token",
		Scopes: []string{
			"https://outlook.office.com/IMAP.AccessAsUser.All",
			"https://outlook.office.com/SMTP.Send",
			"offline_access",
		},
		DeviceAuthURL: base + "/devicecode",
	}
}

// Client performs the flow for one account. ClientSecret is set for a Google
// "Desktop app" client (Google issues one and the token endpoint requires it)
// and left empty for a Microsoft public client, which rejects one.
type Client struct {
	ClientID     string
	ClientSecret string
	Spec         ProviderSpec

	// HTTP overrides the transport, for tests. nil means a client with a
	// 30 second timeout.
	HTTP *http.Client
	// OpenURL hands the authorization URL to the system browser. nil means
	// the caller shows the URL and the user opens it.
	OpenURL func(string) error
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Token is one account's stored credential. Only AccessToken travels to the
// mail server; RefreshToken buys the next one.
type Token struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token,omitempty"`
	TokenType    string    `json:"token_type,omitempty"`
	Expiry       time.Time `json:"expiry"`
	Scope        string    `json:"scope,omitempty"`
}

// leeway is how much life an access token must have left to be worth using:
// enough to finish a login and a first command without racing the expiry.
const leeway = 60 * time.Second

// Valid reports whether the access token is usable for at least a minute. A
// token with no recorded expiry is treated as expired, so it is refreshed
// rather than presented to a server that would reject it.
func (t Token) Valid(now time.Time) bool {
	if t.AccessToken == "" || t.Expiry.IsZero() {
		return false
	}
	return !t.Expiry.Before(now.Add(leeway))
}

// ErrReauth marks a failure that only a fresh sign-in can fix: the refresh
// token was revoked, expired, or belongs to another client.
var ErrReauth = errors.New("oauth: the refresh token is no longer valid; sign in again")

// Error is an RFC 6749 error response from the authorization or token
// endpoint.
type Error struct {
	Code        string
	Description string
	HTTPStatus  int
}

func (e *Error) Error() string {
	msg := "oauth: " + e.Code
	if e.Description != "" {
		msg += ": " + e.Description
	}
	if e.HTTPStatus != 0 {
		msg += fmt.Sprintf(" (http %d)", e.HTTPStatus)
	}
	return msg
}

// Unwrap maps the one error code that means "the stored token is dead" onto
// ErrReauth, so callers can tell it apart from a transient failure with
// errors.Is.
func (e *Error) Unwrap() error {
	if e.Code == "invalid_grant" {
		return ErrReauth
	}
	return nil
}

// loginTimeout caps how long the loopback listener waits for the browser.
const loginTimeout = 5 * time.Minute

// Login runs the authorization-code flow with PKCE (S256) over a loopback
// redirect. onURL is called with the authorization URL before the browser is
// opened, so a UI can print it for copying when the browser does not start.
// It returns when the browser has come back, the context is cancelled, or
// five minutes have passed.
func (c *Client) Login(ctx context.Context, onURL func(string)) (Token, error) {
	if c.ClientID == "" {
		return Token{}, errors.New("oauth: no client id configured")
	}
	if c.Spec.AuthURL == "" || c.Spec.TokenURL == "" {
		return Token{}, errors.New("oauth: provider has no authorization or token endpoint")
	}

	verifier, err := randomString()
	if err != nil {
		return Token{}, err
	}
	state, err := randomString()
	if err != nil {
		return Token{}, err
	}

	// Loopback only: the redirect carries the authorization code, so nothing
	// off this machine may reach the listener.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return Token{}, fmt.Errorf("oauth: opening the loopback listener: %w", err)
	}
	redirect := "http://" + ln.Addr().String() + "/callback"

	ctx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	results := make(chan callbackResult, 1)
	srv := &http.Server{
		Handler:           callbackHandler(state, results),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go srv.Serve(ln)
	// Shutdown, not Close: the handler has already written and flushed the
	// browser's page by the time a result arrives, and Shutdown lets that
	// connection finish rather than cutting it off mid-response. The two
	// second cap is its own, since the login context may already be done.
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer stopCancel()
		_ = srv.Shutdown(stopCtx)
	}()

	authURL := c.authURL(redirect, state, challenge(verifier))
	if onURL != nil {
		onURL(authURL)
	}
	if c.OpenURL != nil {
		// A browser that will not start is not fatal: the URL was handed to
		// onURL and the user can open it by hand.
		_ = c.OpenURL(authURL)
	}

	select {
	case res := <-results:
		if res.err != nil {
			return Token{}, res.err
		}
		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {res.code},
			"code_verifier": {verifier},
			"redirect_uri":  {redirect},
		}
		return c.exchange(ctx, form)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Token{}, errors.New("oauth: timed out waiting for the browser sign-in")
		}
		return Token{}, ctx.Err()
	}
}

// Refresh trades a refresh token for a new access token. Google does not
// rotate refresh tokens and omits one from the response, so the old one is
// carried forward; Microsoft rotates and the new one replaces it.
func (c *Client) Refresh(ctx context.Context, t Token) (Token, error) {
	if t.RefreshToken == "" {
		return Token{}, fmt.Errorf("oauth: no refresh token stored: %w", ErrReauth)
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {t.RefreshToken},
	}
	next, err := c.exchange(ctx, form)
	if err != nil {
		return Token{}, err
	}
	if next.RefreshToken == "" {
		next.RefreshToken = t.RefreshToken
	}
	if next.Scope == "" {
		next.Scope = t.Scope
	}
	return next, nil
}

// authURL builds the authorization request. Google needs access_type=offline
// and prompt=consent or it hands back an access token with no refresh token
// on every sign-in after the first; Microsoft gets the same effect from the
// offline_access scope.
func (c *Client) authURL(redirect, state, codeChallenge string) string {
	q := url.Values{
		"client_id":             {c.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"state":                 {state},
		"code_challenge":        {codeChallenge},
		"code_challenge_method": {"S256"},
	}
	if len(c.Spec.Scopes) > 0 {
		q.Set("scope", strings.Join(c.Spec.Scopes, " "))
	}
	if c.Spec.Name == "google" {
		q.Set("access_type", "offline")
		q.Set("prompt", "consent")
	}
	sep := "?"
	if strings.Contains(c.Spec.AuthURL, "?") {
		sep = "&"
	}
	return c.Spec.AuthURL + sep + q.Encode()
}

// tokenResponse is the token endpoint's JSON, success and failure alike.
type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	Scope            string `json:"scope"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// maxTokenBody caps what is read from the token endpoint. Real responses are
// well under a kilobyte.
const maxTokenBody = 1 << 20

// exchange POSTs a form-encoded grant to the token endpoint. client_id always
// travels in the body; client_secret only when one is configured, since a
// Microsoft public client rejects the request if it carries one.
func (c *Client) exchange(ctx context.Context, form url.Values) (Token, error) {
	form.Set("client_id", c.ClientID)
	if c.ClientSecret != "" {
		form.Set("client_secret", c.ClientSecret)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Spec.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("oauth: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTokenBody))
	if err != nil {
		return Token{}, fmt.Errorf("oauth: reading the token response: %w", err)
	}

	var tr tokenResponse
	// A malformed body is only worth reporting when the status was a success;
	// otherwise the status code is the more useful thing to say.
	jsonErr := json.Unmarshal(body, &tr)
	if tr.Error != "" {
		return Token{}, &Error{Code: sanitize(tr.Error), Description: sanitize(tr.ErrorDescription), HTTPStatus: resp.StatusCode}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Token{}, &Error{Code: "http_error", Description: http.StatusText(resp.StatusCode), HTTPStatus: resp.StatusCode}
	}
	if jsonErr != nil {
		return Token{}, fmt.Errorf("oauth: parsing the token response: %w", jsonErr)
	}
	if tr.AccessToken == "" {
		return Token{}, errors.New("oauth: the token response carried no access token")
	}

	t := Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Scope:        tr.Scope,
	}
	if tr.ExpiresIn > 0 {
		t.Expiry = c.now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	return t, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 30 * time.Second}
}

func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// callbackResult is what the loopback handler hands back: an authorization
// code, or the reason there is not one.
type callbackResult struct {
	code string
	err  error
}

// callbackHandler serves exactly one request. A request to any other path is
// a 404 and a second matching request a 410; a response whose state does not
// match this session is answered 400 and otherwise ignored, since anything
// can reach a loopback port and a stray request must not be able to end a
// sign-in the user is still in the middle of.
//
// The response is written and flushed before the result is handed to Login,
// so the browser always gets its page: Login shuts the listener down as soon
// as it has a result, and a response still in flight at that point would be
// cut off.
func callbackHandler(state string, results chan<- callbackResult) http.Handler {
	var once sync.Once
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if q.Get("state") != state {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, page("This response does not belong to a sign-in started here."))
			return
		}

		var res callbackResult
		switch {
		case q.Get("error") != "":
			res.err = &Error{Code: sanitize(q.Get("error")), Description: sanitize(q.Get("error_description"))}
		case q.Get("code") == "":
			res.err = errors.New("oauth: the sign-in response carried no authorization code")
		default:
			res.code = q.Get("code")
		}

		handled := false
		once.Do(func() { handled = true })

		switch {
		case !handled:
			w.WriteHeader(http.StatusGone)
			io.WriteString(w, page("This sign-in has already been handled."))
		case res.err != nil:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, page("Sign-in failed. Return to the terminal for the reason."))
		default:
			io.WriteString(w, page("Signed in. You can close this tab."))
		}
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if handled {
			results <- res
		}
	})
}

// page renders the one-line response the browser lands on. It is
// self-contained: no scripts, no fonts, no images, nothing to fetch, and
// nothing from the query string is echoed back into it.
func page(message string) string {
	return "<!doctype html><html lang=\"en\"><head><meta charset=\"utf-8\">" +
		"<title>mailshear</title></head><body style=\"font-family:sans-serif;margin:4rem;text-align:center\">" +
		"<h1>mailshear</h1><p>" + message + "</p></body></html>"
}

// randomString returns 32 bytes of cryptographic randomness in the
// base64url-without-padding alphabet, which is what both the PKCE verifier
// and the state parameter want.
func randomString() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("oauth: reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// challenge is the S256 PKCE transform: base64url(sha256(verifier)), no
// padding.
func challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// sanitize strips what a terminal would act on out of provider-supplied text.
// An error code and an error_description come off the wire and end up printed
// in a panel, so an escape sequence in either would be executed rather than
// read. The same goes for an XOAUTH2 failure challenge.
func sanitize(s string) string {
	return headers.Clean(s)
}
