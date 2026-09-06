// Package unsub executes unsubscribe attempts (one-click POST, plain HTTP
// GET, mailto via SMTP) against the methods identified by internal/headers,
// with per-host rate limiting and a global concurrency cap.
package unsub

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Rake-Pro/mailshear/internal/config"
	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/oauth"
)

type Status string

const (
	StatusOK       Status = "ok"
	StatusProbable Status = "probable"
	StatusManual   Status = "manual"
	StatusFailed   Status = "failed"
)

// Target is one sender's best unsubscribe candidate, as classified at scan
// time by internal/headers.
type Target struct {
	SenderKey string
	Display   string
	URIs      []string // newest message's List-Unsubscribe URIs, in header order
	OneClick  bool
	Method    headers.Method
}

// Result is the outcome of one attempt against one Target.
type Result struct {
	SenderKey  string
	Display    string
	Method     headers.Method // method actually attempted (may be lower than Target.Method if preferred URI missing)
	URI        string         // URI attempted, "" for manual
	Status     Status
	HTTPStatus int
	FinalURL   string // after redirects, for http GET
	Err        string
	Duration   time.Duration
}

type SMTP struct {
	Host     string
	Port     int
	Username string
	Password string
	// AccessToken authenticates with SASL XOAUTH2 instead of PLAIN. When it
	// is set Password is ignored.
	AccessToken string
	From        string
}

type Options struct {
	HTTPGet         bool          // attempt plain http(s) GET links when no one-click
	FollowRedirects int           // for GET; 0 = none
	Timeout         time.Duration // per request, default 15s
	PerHostRPS      float64       // default 1
	MaxInFlight     int           // default 4
	UserAgent       string        // default "mailshear/0.1 (+https://github.com/Rake-Pro/mailshear)"
	SMTP            *SMTP         // nil = mailto targets are reported manual
	Client          *http.Client  // optional override for tests; when nil build from Timeout. Redirect policy is set per request kind regardless.
	SendMail        func(ctx context.Context, s SMTP, to, subject, body string) error
	// AllowPrivateHosts permits unsubscribe requests to destinations that
	// resolve inside the local network. Off by default: a List-Unsubscribe
	// header is attacker-controlled, and following one to 127.0.0.1 or
	// 192.168.x.x turns this into a request forger against the user's own
	// machines.
	AllowPrivateHosts bool
}

const (
	defaultTimeout     = 15 * time.Second
	defaultPerHostRPS  = 1.0
	defaultMaxInFlight = 4
	defaultUserAgent   = "mailshear/0.1 (+https://github.com/Rake-Pro/mailshear)"
	maxBodyRead        = 64 * 1024

	privateHostMsg = "destination resolves to a private address; set unsubscribe.allow_private_hosts: true to permit"
)

// Executor runs unsubscribe attempts with per-host rate limiting and a
// global concurrency cap.
type Executor struct {
	opts     Options
	interval time.Duration
	sem      chan struct{}

	mu       sync.Mutex
	limiters map[string]*hostLimiter
}

func New(opts Options) *Executor {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	if opts.PerHostRPS <= 0 {
		opts.PerHostRPS = defaultPerHostRPS
	}
	if opts.MaxInFlight <= 0 {
		opts.MaxInFlight = defaultMaxInFlight
	}
	if opts.UserAgent == "" {
		opts.UserAgent = defaultUserAgent
	}
	return &Executor{
		opts:     opts,
		interval: time.Duration(float64(time.Second) / opts.PerHostRPS),
		sem:      make(chan struct{}, opts.MaxInFlight),
		limiters: make(map[string]*hostLimiter),
	}
}

// Run executes all targets with per-host rate limiting (one request per
// 1/PerHostRPS seconds per destination host) and at most MaxInFlight
// concurrent requests. Calls onResult as each finishes (may be from
// multiple goroutines; the caller serializes). Returns results in target
// order. Never panics on bad input; a target with no usable URI yields
// StatusManual.
func (e *Executor) Run(ctx context.Context, targets []Target, onResult func(Result)) []Result {
	results := make([]Result, len(targets))
	var wg sync.WaitGroup
	wg.Add(len(targets))
	for i := range targets {
		i := i
		t := targets[i]
		go func() {
			defer wg.Done()
			res := e.runOne(ctx, t)
			results[i] = res
			if onResult != nil {
				onResult(res)
			}
		}()
	}
	wg.Wait()
	return results
}

func (e *Executor) runOne(ctx context.Context, t Target) Result {
	start := time.Now()
	res := Result{SenderKey: t.SenderKey, Display: t.Display}
	defer func() { res.Duration = time.Since(start) }()

	if err := ctx.Err(); err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	https, httpOnly, mailtoURIs := classifyURIs(t.URIs)

	var p Result
	switch {
	case t.OneClick && (len(https) > 0 || len(httpOnly) > 0):
		p = e.doOneClick(ctx, firstOf(https, httpOnly))
		// Some senders (Substack among them) answer the one-click POST with a
		// redirect, which the RFC forbids. A plain GET of the same link usually
		// completes the unsubscribe; record it as probable like any other GET.
		if p.Status == StatusFailed && p.HTTPStatus >= 300 && p.HTTPStatus < 400 && e.opts.HTTPGet {
			if g := e.doGet(ctx, firstOf(https, httpOnly)); g.Status != StatusFailed {
				g.Err = fmt.Sprintf("one-click redirected (http %d), fell back to GET", p.HTTPStatus)
				p = g
			}
		}
	case len(https) > 0 || len(httpOnly) > 0:
		if !e.opts.HTTPGet {
			p = Result{Method: headers.MethodHTTP, Status: StatusManual, Err: "http_get disabled"}
		} else {
			p = e.doGet(ctx, firstOf(https, httpOnly))
		}
	case len(mailtoURIs) > 0:
		p = e.doMailto(ctx, mailtoURIs[0])
	default:
		p = Result{Method: headers.MethodNone, Status: StatusManual, Err: "no unsubscribe uri"}
	}

	res.Method = p.Method
	res.URI = p.URI
	res.Status = p.Status
	res.HTTPStatus = p.HTTPStatus
	res.FinalURL = p.FinalURL
	res.Err = p.Err
	return res
}

// firstOf prefers the first https URI, falling back to the first plain-http
// one. Callers only invoke it when at least one of the two is non-empty.
func firstOf(https, httpOnly []string) string {
	if len(https) > 0 {
		return https[0]
	}
	return httpOnly[0]
}

// classifyURIs splits a List-Unsubscribe URI list by scheme, preserving
// header order within each bucket. Unparseable or unrecognized-scheme
// entries are dropped; headers.ParseListUnsubscribe already filters these
// upstream, so this is defense in depth.
func classifyURIs(uris []string) (https, httpOnly, mailtoURIs []string) {
	for _, raw := range uris {
		u, err := url.Parse(raw)
		if err != nil {
			continue
		}
		switch strings.ToLower(u.Scheme) {
		case "https":
			https = append(https, raw)
		case "http":
			httpOnly = append(httpOnly, raw)
		case "mailto":
			mailtoURIs = append(mailtoURIs, raw)
		}
	}
	return
}

// doOneClick POSTs the RFC 8058 one-click body and follows no redirects: a
// 3xx is treated as a violation of the RFC, not a retry opportunity.
func (e *Executor) doOneClick(ctx context.Context, uri string) Result {
	res := Result{Method: headers.MethodOneClick, URI: uri}

	u, err := url.Parse(uri)
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	if err := e.checkHost(ctx, u); err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	release, err := e.throttle(ctx, hostKey(u))
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	defer release()

	client := e.buildClient(func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uri, strings.NewReader("List-Unsubscribe=One-Click"))
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", e.opts.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		res.Status = StatusFailed
		res.Err = DescribeNetError(err)
		return res
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))

	res.HTTPStatus = resp.StatusCode
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Status = StatusOK
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		res.Status = StatusFailed
		res.Err = "one-click endpoint redirected (RFC 8058 forbids this)"
	default:
		res.Status = StatusFailed
		res.Err = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return res
}

// doGet follows up to Options.FollowRedirects redirects. Success is
// recorded as probable, never ok: a 2xx here often just means a page
// requiring a further click was reached.
func (e *Executor) doGet(ctx context.Context, uri string) Result {
	res := Result{Method: headers.MethodHTTP, URI: uri}

	u, err := url.Parse(uri)
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	if err := e.checkHost(ctx, u); err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	release, err := e.throttle(ctx, hostKey(u))
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	defer release()

	max := e.opts.FollowRedirects
	client := e.buildClient(func(req *http.Request, via []*http.Request) error {
		if len(via) >= max {
			return fmt.Errorf("stopped after %d redirects", max)
		}
		// Every hop is re-checked: a public host can redirect inward.
		return e.checkHost(req.Context(), req.URL)
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	req.Header.Set("User-Agent", e.opts.UserAgent)

	resp, err := client.Do(req)
	if err != nil {
		res.Status = StatusFailed
		res.Err = DescribeNetError(err)
		return res
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyRead))

	res.HTTPStatus = resp.StatusCode
	if resp.Request != nil && resp.Request.URL != nil {
		res.FinalURL = resp.Request.URL.String()
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.Status = StatusProbable
	} else {
		res.Status = StatusFailed
		res.Err = fmt.Sprintf("http %d", resp.StatusCode)
	}
	return res
}

// doMailto sends via SMTP when configured, otherwise reports manual.
func (e *Executor) doMailto(ctx context.Context, uri string) Result {
	res := Result{Method: headers.MethodMailto}

	if e.opts.SMTP == nil {
		res.Status = StatusManual
		res.Err = "smtp not configured"
		return res
	}

	addr, subject, body, ok := headers.MailtoParts(uri)
	if !ok || addr == "" {
		res.URI = uri
		res.Status = StatusFailed
		res.Err = "invalid mailto uri"
		return res
	}
	if subject == "" {
		subject = "unsubscribe"
	}
	if body == "" {
		body = "unsubscribe"
	}

	key := "smtp:" + strings.ToLower(e.opts.SMTP.Host) + ":" + strconv.Itoa(e.opts.SMTP.Port)
	release, err := e.throttle(ctx, key)
	if err != nil {
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}
	defer release()

	sendMail := e.opts.SendMail
	if sendMail == nil {
		sendMail = defaultSendMail
	}
	if err := sendMail(ctx, *e.opts.SMTP, addr, subject, body); err != nil {
		res.URI = uri
		res.Status = StatusFailed
		res.Err = err.Error()
		return res
	}

	res.URI = uri
	res.Status = StatusOK
	return res
}

// buildClient returns an *http.Client with the given redirect policy. If
// Options.Client is set it is shallow-copied so its Transport (a test
// double, typically) is kept, but the redirect policy is always ours:
// one-click and GET need different, incompatible policies regardless of
// what a caller-supplied client would otherwise do.
func (e *Executor) buildClient(checkRedirect func(req *http.Request, via []*http.Request) error) *http.Client {
	if e.opts.Client != nil {
		c := *e.opts.Client
		c.CheckRedirect = checkRedirect
		return &c
	}
	return &http.Client{Timeout: e.opts.Timeout, CheckRedirect: checkRedirect}
}

// checkHost refuses a destination that resolves to an address on the local
// machine or the local network, unless the caller opted in. The lookup
// happens per request (and per redirect hop), so a name that resolves
// publicly at scan time but privately now is still caught.
func (e *Executor) checkHost(ctx context.Context, u *url.URL) error {
	if e.opts.AllowPrivateHosts {
		return nil
	}
	host := u.Hostname()
	if host == "" {
		return errors.New(privateHostMsg)
	}
	var ips []net.IP
	if ip := net.ParseIP(host); ip != nil {
		ips = []net.IP{ip}
	} else {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return err
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	if len(ips) == 0 {
		return errors.New(privateHostMsg)
	}
	// Any private answer refuses the whole request: a name that resolves to
	// both is a rebinding attempt, not a usable endpoint.
	for _, ip := range ips {
		if isPrivateIP(ip) {
			return errors.New(privateHostMsg)
		}
	}
	return nil
}

func isPrivateIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast()
}

// hostKey returns the rate-limit bucket key for a URL. Per spec this is
// the lowercase host with the port stripped, EXCEPT when the host is an IP
// literal: several distinct services (e.g. httptest servers under test, or
// self-hosted unsubscribe endpoints on the same box) can share an IP with
// only the port distinguishing them, and stripping the port there would
// wrongly serialize unrelated destinations. For IP-literal hosts the key
// keeps host:port instead.
func hostKey(u *url.URL) string {
	h := strings.ToLower(u.Hostname())
	if net.ParseIP(h) != nil {
		return strings.ToLower(u.Host)
	}
	return h
}

// throttle blocks until both the per-host rate limit and the global
// concurrency cap admit one request, returning a release func to call when
// the request is done (it only frees the concurrency slot; the per-host
// pacing slot was already consumed by the wait).
func (e *Executor) throttle(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := e.limiterFor(key).wait(ctx, e.interval); err != nil {
		return nil, err
	}
	if err := e.acquireSem(ctx); err != nil {
		return nil, err
	}
	return e.releaseSem, nil
}

func (e *Executor) limiterFor(key string) *hostLimiter {
	e.mu.Lock()
	defer e.mu.Unlock()
	l, ok := e.limiters[key]
	if !ok {
		l = &hostLimiter{}
		e.limiters[key] = l
	}
	return l
}

func (e *Executor) acquireSem(ctx context.Context) error {
	select {
	case e.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Executor) releaseSem() {
	<-e.sem
}

// hostLimiter reserves evenly spaced slots (interval apart) for one rate
// limit bucket. Reservation happens under the lock so concurrent callers
// each get a distinct, increasing slot; the actual sleep happens outside
// the lock so it does not block other buckets.
type hostLimiter struct {
	mu   sync.Mutex
	next time.Time
}

func (l *hostLimiter) wait(ctx context.Context, interval time.Duration) error {
	l.mu.Lock()
	start := time.Now()
	if l.next.After(start) {
		start = l.next
	}
	l.next = start.Add(interval)
	l.mu.Unlock()

	d := time.Until(start)
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// defaultSendMail sends a minimal plain-text unsubscribe message over
// SMTP with STARTTLS and, when a username is set, PLAIN auth.
func defaultSendMail(ctx context.Context, s SMTP, to, subject, body string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	addr := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer c.Close()

	if ok, _ := c.Extension("STARTTLS"); ok {
		if err := c.StartTLS(&tls.Config{ServerName: s.Host}); err != nil {
			return err
		}
	}

	from := s.From
	if from == "" {
		from = s.Username
	}

	switch {
	case s.AccessToken != "":
		if err := c.Auth(oauth.SMTPAuth(s.Username, s.AccessToken)); err != nil {
			return err
		}
	case s.Username != "":
		if err := c.Auth(smtp.PlainAuth("", s.Username, s.Password, s.Host)); err != nil {
			return err
		}
	}

	if err := c.Mail(from); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}

	msgID, err := generateMessageID(s.Host)
	if err != nil {
		w.Close()
		return err
	}

	// Defense in depth: headers.MailtoParts already stripped these, but this
	// function is exported through Options.SendMail and the subject is
	// attacker-controlled, so nothing that could open a new header survives.
	subject = headers.StripControl(subject)
	body = toCRLF(body)

	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", to)
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&buf, "Message-ID: %s\r\n", msgID)
	buf.WriteString("MIME-Version: 1.0\r\n")
	buf.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	buf.WriteString("Auto-Submitted: auto-generated\r\n")
	buf.WriteString("\r\n")
	buf.WriteString(body)
	buf.WriteString("\r\n")

	if _, err := w.Write(buf.Bytes()); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// toCRLF normalizes a body to CRLF line endings and drops stray controls.
// net/smtp's DATA writer does the dot-stuffing.
func toCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	return strings.ReplaceAll(s, "\n", "\r\n")
}

func generateMessageID(host string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf("<%x@%s>", b, host), nil
}

// DescribeNetError turns transport errors into one-line explanations. Tracking
// hosts are commonly sinkholed by DNS filters such as Pi-hole, which shows up
// as a lookup failure or a refused connection to the filter's block address.
func DescribeNetError(err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return fmt.Sprintf("dns lookup of %s failed (%s); a DNS filter such as Pi-hole may block this tracking host, retry from an unfiltered network or open the link manually", dnsErr.Name, shortErr(dnsErr))
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return fmt.Sprintf("connection to %s refused or unreachable; if a DNS filter such as Pi-hole answers for this host, retry from an unfiltered network or open the link manually", opErr.Addr)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timed out"
	}
	return shortErr(err)
}

func shortErr(err error) string {
	msg := err.Error()
	if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, "Post \"") || strings.HasPrefix(msg, "Get \"") {
		// drop the quoted URL prefix net/http adds; the URI is reported separately
		if j := strings.LastIndex(msg, "\": "); j > 0 {
			msg = msg[j+3:]
		}
	}
	if len(msg) > 160 {
		msg = msg[:157] + "..."
	}
	return msg
}

// Secret is the account credential SMTP reuses for mailto unsubscribes:
// either the app password or, for an OAuth account, the access token.
// Exactly one of the two is set.
type Secret struct {
	Password    string
	AccessToken string
}

// FromConfig builds an executor from the unsubscribe configuration. SMTP,
// when the account configures it, reuses the account's IMAP credential,
// which is what every provider expects on both the password and the OAuth
// path.
func FromConfig(o config.UnsubscribeOpts, acct config.Account, sec Secret) *Executor {
	opts := Options{
		HTTPGet:           o.HTTPGet,
		FollowRedirects:   o.FollowRedirects,
		Timeout:           time.Duration(o.TimeoutSeconds) * time.Second,
		PerHostRPS:        o.PerHostRPS,
		MaxInFlight:       defaultMaxInFlight,
		AllowPrivateHosts: o.AllowPrivateHosts,
	}
	if acct.SMTP != nil && acct.SMTP.Host != "" {
		port := acct.SMTP.Port
		if port == 0 {
			port = 587
		}
		opts.SMTP = &SMTP{
			Host:        acct.SMTP.Host,
			Port:        port,
			Username:    acct.Username,
			Password:    sec.Password,
			AccessToken: sec.AccessToken,
			From:        acct.Username,
		}
	}
	return New(opts)
}
