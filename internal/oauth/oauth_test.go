package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestChallengeIsS256OfVerifier(t *testing.T) {
	// RFC 7636 appendix B's worked example.
	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	const want = "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := challenge(verifier); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}

	// And it is the plain transform for an arbitrary verifier too.
	sum := sha256.Sum256([]byte("another-verifier"))
	if got, want := challenge("another-verifier"), base64.RawURLEncoding.EncodeToString(sum[:]); got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestRandomStringIsDistinctAndURLSafe(t *testing.T) {
	a, err := randomString()
	if err != nil {
		t.Fatalf("randomString: %v", err)
	}
	b, err := randomString()
	if err != nil {
		t.Fatalf("randomString: %v", err)
	}
	if a == b {
		t.Fatal("two random strings came back equal")
	}
	if len(a) != 43 {
		t.Fatalf("length = %d, want 43 (32 bytes, base64url unpadded)", len(a))
	}
	if strings.ContainsAny(a, "+/=") {
		t.Fatalf("%q is not base64url without padding", a)
	}
}

// fakeProvider is an authorization server: it records the authorization
// request, and answers the token endpoint from a caller-supplied handler.
type fakeProvider struct {
	srv *httptest.Server

	authQuery url.Values
	tokenForm url.Values

	tokenJSON   string
	tokenStatus int
}

func newFakeProvider(t *testing.T) *fakeProvider {
	t.Helper()
	p := &fakeProvider{tokenStatus: http.StatusOK}
	mux := http.NewServeMux()
	mux.HandleFunc("/auth", func(w http.ResponseWriter, r *http.Request) {
		p.authQuery = r.URL.Query()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token: parse form: %v", err)
		}
		p.tokenForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(p.tokenStatus)
		w.Write([]byte(p.tokenJSON))
	})
	p.srv = httptest.NewServer(mux)
	t.Cleanup(p.srv.Close)
	return p
}

func (p *fakeProvider) spec() ProviderSpec {
	return ProviderSpec{
		Name:     "fake",
		AuthURL:  p.srv.URL + "/auth",
		TokenURL: p.srv.URL + "/token",
		Scopes:   []string{"mail.read", "mail.send"},
	}
}

// browse plays the browser: it fetches the authorization URL (so the fake
// provider records the request), then calls back to the loopback listener
// with the query the caller asks for. It returns the callback's status.
func browse(t *testing.T, authURL string, extra url.Values) int {
	t.Helper()
	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatalf("parse auth url: %v", err)
	}
	resp, err := http.Get(authURL)
	if err != nil {
		t.Fatalf("get auth url: %v", err)
	}
	resp.Body.Close()

	q := u.Query()
	cb, err := url.Parse(q.Get("redirect_uri"))
	if err != nil {
		t.Fatalf("parse redirect_uri: %v", err)
	}
	values := url.Values{}
	for k, v := range extra {
		values[k] = v
	}
	if _, ok := values["state"]; !ok {
		values.Set("state", q.Get("state"))
	}
	cb.RawQuery = values.Encode()

	resp, err = http.Get(cb.String())
	if err != nil {
		t.Fatalf("get callback: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestLoginEndToEnd(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenJSON = `{"access_token":"at-1","refresh_token":"rt-1","token_type":"Bearer","expires_in":3600,"scope":"mail.read mail.send"}`

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	c := &Client{
		ClientID:     "client-1",
		ClientSecret: "secret-1",
		Spec:         p.spec(),
		Now:          func() time.Time { return now },
	}

	var gotURL string
	done := make(chan struct{})
	c.OpenURL = func(u string) error {
		gotURL = u
		go func() {
			defer close(done)
			_ = browse(t, u, url.Values{"code": {"the-code"}})
		}()
		return nil
	}

	var seen string
	tok, err := c.Login(context.Background(), func(u string) { seen = u })
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	<-done

	if seen == "" || seen != gotURL {
		t.Fatalf("onURL got %q, browser got %q", seen, gotURL)
	}
	if tok.AccessToken != "at-1" || tok.RefreshToken != "rt-1" {
		t.Fatalf("token = %+v", tok)
	}
	if want := now.Add(time.Hour); !tok.Expiry.Equal(want) {
		t.Fatalf("expiry = %v, want %v", tok.Expiry, want)
	}

	// The authorization request carried PKCE, the loopback redirect and the
	// scopes.
	q := p.authQuery
	if q.Get("response_type") != "code" || q.Get("client_id") != "client-1" {
		t.Fatalf("auth query = %v", q)
	}
	if q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" {
		t.Fatalf("no S256 PKCE challenge: %v", q)
	}
	if q.Get("scope") != "mail.read mail.send" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
	redirect := q.Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") || !strings.HasSuffix(redirect, "/callback") {
		t.Fatalf("redirect_uri = %q, want a loopback callback", redirect)
	}

	// The exchange echoed the same redirect and a verifier matching the
	// challenge, and carried the secret because one is configured.
	f := p.tokenForm
	if f.Get("grant_type") != "authorization_code" || f.Get("code") != "the-code" {
		t.Fatalf("token form = %v", f)
	}
	if f.Get("redirect_uri") != redirect {
		t.Fatalf("token redirect_uri = %q, want %q", f.Get("redirect_uri"), redirect)
	}
	if got := challenge(f.Get("code_verifier")); got != q.Get("code_challenge") {
		t.Fatalf("verifier does not hash to the challenge: %q vs %q", got, q.Get("code_challenge"))
	}
	if f.Get("client_secret") != "secret-1" {
		t.Fatalf("client_secret = %q", f.Get("client_secret"))
	}
}

// A loopback port is reachable by anything on the machine, so a request with
// the wrong state is answered 400 and otherwise ignored: it must not be able
// to end a sign-in the user is still in the middle of. The real callback,
// arriving afterwards, still completes the login.
func TestLoginIgnoresAWrongStateAndThenSucceeds(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenJSON = `{"access_token":"at-1","expires_in":3600}`

	c := &Client{ClientID: "client-1", Spec: p.spec()}
	done := make(chan struct{})
	var strayStatus, realStatus int
	c.OpenURL = func(u string) error {
		go func() {
			defer close(done)
			strayStatus = browse(t, u, url.Values{"code": {"stray"}, "state": {"not-the-state"}})
			realStatus = browse(t, u, url.Values{"code": {"the-code"}})
		}()
		return nil
	}

	tok, err := c.Login(context.Background(), nil)
	<-done
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if strayStatus != http.StatusBadRequest {
		t.Fatalf("wrong-state callback status = %d, want 400", strayStatus)
	}
	if realStatus != http.StatusOK {
		t.Fatalf("matching callback status = %d, want 200", realStatus)
	}
	if tok.AccessToken != "at-1" {
		t.Fatalf("token = %+v", tok)
	}
	if p.tokenForm.Get("code") != "the-code" {
		t.Fatalf("exchanged code = %q, want the one from the matching callback", p.tokenForm.Get("code"))
	}
}

func TestLoginMapsExchangeError(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenStatus = http.StatusBadRequest
	p.tokenJSON = `{"error":"invalid_client","error_description":"bad client id"}`

	c := &Client{ClientID: "client-1", Spec: p.spec()}
	done := make(chan struct{})
	c.OpenURL = func(u string) error {
		go func() {
			defer close(done)
			_ = browse(t, u, url.Values{"code": {"the-code"}})
		}()
		return nil
	}

	_, err := c.Login(context.Background(), nil)
	<-done

	var oerr *Error
	if !errors.As(err, &oerr) {
		t.Fatalf("error = %v (%T), want *oauth.Error", err, err)
	}
	if oerr.Code != "invalid_client" || oerr.Description != "bad client id" || oerr.HTTPStatus != http.StatusBadRequest {
		t.Fatalf("error = %+v", oerr)
	}
	if errors.Is(err, ErrReauth) {
		t.Fatal("invalid_client should not read as a re-auth")
	}
}

func TestLoginSurfacesProviderErrorFromCallback(t *testing.T) {
	p := newFakeProvider(t)
	c := &Client{ClientID: "client-1", Spec: p.spec()}
	done := make(chan struct{})
	c.OpenURL = func(u string) error {
		go func() {
			defer close(done)
			_ = browse(t, u, url.Values{"error": {"access_denied"}, "error_description": {"user said no"}})
		}()
		return nil
	}

	_, err := c.Login(context.Background(), nil)
	<-done

	var oerr *Error
	if !errors.As(err, &oerr) || oerr.Code != "access_denied" {
		t.Fatalf("error = %v, want access_denied", err)
	}
}

func TestLoginCancelledByContext(t *testing.T) {
	p := newFakeProvider(t)
	c := &Client{ClientID: "client-1", Spec: p.spec()}

	ctx, cancel := context.WithCancel(context.Background())
	c.OpenURL = func(string) error {
		cancel()
		return nil
	}
	if _, err := c.Login(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestRefreshKeepsTheRefreshTokenWhenOmitted(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenJSON = `{"access_token":"at-2","token_type":"Bearer","expires_in":3599}`

	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	c := &Client{ClientID: "client-1", Spec: p.spec(), Now: func() time.Time { return now }}

	got, err := c.Refresh(context.Background(), Token{
		AccessToken:  "at-1",
		RefreshToken: "rt-1",
		Scope:        "mail.read",
		Expiry:       now.Add(-time.Minute),
	})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.AccessToken != "at-2" {
		t.Fatalf("access token = %q", got.AccessToken)
	}
	if got.RefreshToken != "rt-1" {
		t.Fatalf("refresh token = %q, want the stored one carried forward", got.RefreshToken)
	}
	if got.Scope != "mail.read" {
		t.Fatalf("scope = %q, want the stored one carried forward", got.Scope)
	}
	if want := now.Add(3599 * time.Second); !got.Expiry.Equal(want) {
		t.Fatalf("expiry = %v, want %v", got.Expiry, want)
	}
	// No client secret configured, so none was sent.
	if _, ok := p.tokenForm["client_secret"]; ok {
		t.Fatal("client_secret was sent for a public client")
	}
	if p.tokenForm.Get("grant_type") != "refresh_token" || p.tokenForm.Get("refresh_token") != "rt-1" {
		t.Fatalf("token form = %v", p.tokenForm)
	}
}

func TestRefreshRotationReplacesTheRefreshToken(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenJSON = `{"access_token":"at-2","refresh_token":"rt-2","expires_in":60}`

	c := &Client{ClientID: "client-1", Spec: p.spec()}
	got, err := c.Refresh(context.Background(), Token{RefreshToken: "rt-1"})
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got.RefreshToken != "rt-2" {
		t.Fatalf("refresh token = %q, want the rotated one", got.RefreshToken)
	}
}

func TestRefreshInvalidGrantIsReauth(t *testing.T) {
	p := newFakeProvider(t)
	p.tokenStatus = http.StatusBadRequest
	p.tokenJSON = `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`

	c := &Client{ClientID: "client-1", Spec: p.spec()}
	_, err := c.Refresh(context.Background(), Token{RefreshToken: "rt-1"})
	if !errors.Is(err, ErrReauth) {
		t.Fatalf("error = %v, want it to read as ErrReauth", err)
	}
	var oerr *Error
	if !errors.As(err, &oerr) || oerr.Code != "invalid_grant" {
		t.Fatalf("error = %v, want an *oauth.Error with invalid_grant", err)
	}
}

func TestRefreshWithoutATokenIsReauth(t *testing.T) {
	c := &Client{ClientID: "client-1", Spec: Google()}
	if _, err := c.Refresh(context.Background(), Token{}); !errors.Is(err, ErrReauth) {
		t.Fatalf("error = %v, want ErrReauth", err)
	}
}

func TestTokenValidBoundary(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		tok  Token
		want bool
	}{
		{"an hour left", Token{AccessToken: "a", Expiry: now.Add(time.Hour)}, true},
		{"exactly the leeway", Token{AccessToken: "a", Expiry: now.Add(leeway)}, true},
		{"a second inside the leeway", Token{AccessToken: "a", Expiry: now.Add(leeway - time.Second)}, false},
		{"already expired", Token{AccessToken: "a", Expiry: now.Add(-time.Second)}, false},
		{"no expiry recorded", Token{AccessToken: "a"}, false},
		{"no access token", Token{Expiry: now.Add(time.Hour)}, false},
	}
	for _, tc := range cases {
		if got := tc.tok.Valid(now); got != tc.want {
			t.Errorf("%s: Valid = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGoogleAuthURLAsksForOfflineAccess(t *testing.T) {
	c := &Client{ClientID: "client-1", Spec: Google()}
	raw := c.authURL("http://127.0.0.1:1234/callback", "st", "ch")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if u.Host != "accounts.google.com" || u.Path != "/o/oauth2/v2/auth" {
		t.Fatalf("url = %q", raw)
	}
	q := u.Query()
	if q.Get("access_type") != "offline" || q.Get("prompt") != "consent" {
		t.Fatalf("google auth url does not ask for a refresh token: %v", q)
	}
	if q.Get("scope") != "https://mail.google.com/" {
		t.Fatalf("scope = %q", q.Get("scope"))
	}
}

func TestMicrosoftSpecTenants(t *testing.T) {
	def := Microsoft("")
	if !strings.Contains(def.AuthURL, "/common/oauth2/v2.0/authorize") {
		t.Fatalf("auth url = %q", def.AuthURL)
	}
	if !strings.Contains(def.TokenURL, "/common/oauth2/v2.0/token") {
		t.Fatalf("token url = %q", def.TokenURL)
	}
	want := []string{
		"https://outlook.office.com/IMAP.AccessAsUser.All",
		"https://outlook.office.com/SMTP.Send",
		"offline_access",
	}
	if strings.Join(def.Scopes, " ") != strings.Join(want, " ") {
		t.Fatalf("scopes = %v", def.Scopes)
	}
	named := Microsoft("contoso.onmicrosoft.com")
	if !strings.Contains(named.AuthURL, "/contoso.onmicrosoft.com/") {
		t.Fatalf("auth url = %q", named.AuthURL)
	}
	// A microsoft public client sends no secret, so nothing here implies one.
	if named.Name != "microsoft" {
		t.Fatalf("name = %q", named.Name)
	}
}

func TestLoginNeedsAClientID(t *testing.T) {
	c := &Client{Spec: Google()}
	if _, err := c.Login(context.Background(), nil); err == nil {
		t.Fatal("Login ran without a client id")
	}
}

// The one-shot is spent by a matching state only, so a stray request cannot
// burn it and leave the browser's real callback answered 410.
func TestCallbackStateMismatchDoesNotSpendTheOneShot(t *testing.T) {
	results := make(chan callbackResult, 1)
	srv := httptest.NewServer(callbackHandler("st", results))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?state=wrong&code=stray")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("wrong-state status = %d, want 400", resp.StatusCode)
	}
	select {
	case got := <-results:
		t.Fatalf("a wrong-state request produced a result: %+v", got)
	default:
	}

	resp, err = http.Get(srv.URL + "/callback?state=st&code=c1")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("matching status = %d, want 200", resp.StatusCode)
	}
	if got := <-results; got.code != "c1" {
		t.Fatalf("result = %+v", got)
	}
}

// The provider controls error and error_description, and both are printed in
// a terminal panel, so the escape sequences are removed as the error is built.
func TestCallbackErrorTextIsSanitized(t *testing.T) {
	results := make(chan callbackResult, 1)
	srv := httptest.NewServer(callbackHandler("st", results))
	defer srv.Close()

	q := url.Values{
		"state":             {"st"},
		"error":             {"access_denied"},
		"error_description": {"denied\x1b]0;pwned\x07 by\x1b[2J policy"},
	}
	resp, err := http.Get(srv.URL + "/callback?" + q.Encode())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()

	res := <-results
	var oe *Error
	if !errors.As(res.err, &oe) {
		t.Fatalf("err = %v, want an *Error", res.err)
	}
	if oe.Description != "denied by policy" {
		t.Fatalf("description = %q, want the escape sequences gone", oe.Description)
	}
	if strings.ContainsAny(res.err.Error(), "\x1b\x07") {
		t.Fatalf("error text still carries control characters: %q", res.err.Error())
	}
}

func TestCallbackHandlerAcceptsOneRequest(t *testing.T) {
	results := make(chan callbackResult, 1)
	h := callbackHandler("st", results)
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/callback?state=st&code=c1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := <-results; got.code != "c1" {
		t.Fatalf("result = %+v", got)
	}

	resp, err = http.Get(srv.URL + "/callback?state=st&code=c2")
	if err != nil {
		t.Fatalf("second get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusGone {
		t.Fatalf("second request status = %d, want 410", resp.StatusCode)
	}
	select {
	case got := <-results:
		t.Fatalf("a second result was delivered: %+v", got)
	default:
	}

	resp, err = http.Get(srv.URL + "/elsewhere")
	if err != nil {
		t.Fatalf("other path: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other path status = %d, want 404", resp.StatusCode)
	}
}

func TestCallbackPageIsSelfContained(t *testing.T) {
	html := page("Signed in. You can close this tab.")
	for _, bad := range []string{"http://", "https://", "<script", "<img", "<link"} {
		if strings.Contains(strings.ToLower(html), bad) {
			t.Fatalf("callback page contains %q: %s", bad, html)
		}
	}
}
