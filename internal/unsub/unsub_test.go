package unsub_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/unsub"
)

func TestOneClickSuccess(t *testing.T) {
	var gotMethod, gotBody, gotCT, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		gotCT = r.Header.Get("Content-Type")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{
		SenderKey: "s1",
		OneClick:  true,
		URIs:      []string{srv.URL + "/unsub"},
	}}
	results := e.Run(context.Background(), targets, nil)

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Status != unsub.StatusOK {
		t.Fatalf("status = %q, want ok (err=%q)", r.Status, r.Err)
	}
	if r.Method != headers.MethodOneClick {
		t.Fatalf("method = %q, want oneclick", r.Method)
	}
	if gotMethod != http.MethodPost {
		t.Fatalf("request method = %q, want POST", gotMethod)
	}
	if gotBody != "List-Unsubscribe=One-Click" {
		t.Fatalf("body = %q, want exact one-click body", gotBody)
	}
	if !strings.EqualFold(gotCT, "application/x-www-form-urlencoded") {
		t.Fatalf("content-type = %q, want urlencoded", gotCT)
	}
	if gotUA == "" {
		t.Fatalf("User-Agent was empty")
	}
}

func TestOneClickRedirectIsFailure(t *testing.T) {
	var redirectHit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/unsub":
			w.Header().Set("Location", "/redirected")
			w.WriteHeader(http.StatusFound)
		case "/redirected":
			redirectHit = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{OneClick: true, URIs: []string{srv.URL + "/unsub"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
	if r.HTTPStatus != http.StatusFound {
		t.Fatalf("HTTPStatus = %d, want 302", r.HTTPStatus)
	}
	if !strings.Contains(r.Err, "RFC 8058") {
		t.Fatalf("err = %q, want mention of RFC 8058", r.Err)
	}
	if redirectHit {
		t.Fatalf("redirect target was requested; one-click must not follow redirects")
	}
}

func TestOneClickServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{OneClick: true, URIs: []string{srv.URL + "/unsub"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
	if r.HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("HTTPStatus = %d, want 500", r.HTTPStatus)
	}
}

func TestHTTPGetSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "landing page")
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true, FollowRedirects: 5})
	targets := []unsub.Target{{URIs: []string{srv.URL + "/list"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusProbable {
		t.Fatalf("status = %q, want probable (err=%q)", r.Status, r.Err)
	}
	if r.FinalURL != srv.URL+"/list" {
		t.Fatalf("FinalURL = %q, want %q", r.FinalURL, srv.URL+"/list")
	}
}

// chainServer serves a redirect chain of exactly n hops: /step/0 redirects
// to /step/1, ..., /step/(n-1) redirects to /step/n, and /step/n returns
// 200. The starting URL is /step/0.
func chainServer(n int) *httptest.Server {
	mux := http.NewServeMux()
	for i := 0; i < n; i++ {
		next := fmt.Sprintf("/step/%d", i+1)
		mux.HandleFunc(fmt.Sprintf("/step/%d", i), func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", next)
			w.WriteHeader(http.StatusFound)
		})
	}
	mux.HandleFunc(fmt.Sprintf("/step/%d", n), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return httptest.NewServer(mux)
}

func TestHTTPGetRedirectsWithinLimit(t *testing.T) {
	srv := chainServer(2)
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true, FollowRedirects: 5})
	targets := []unsub.Target{{URIs: []string{srv.URL + "/step/0"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusProbable {
		t.Fatalf("status = %q, want probable (err=%q)", r.Status, r.Err)
	}
	want := srv.URL + "/step/2"
	if r.FinalURL != want {
		t.Fatalf("FinalURL = %q, want %q", r.FinalURL, want)
	}
}

func TestHTTPGetRedirectsExceedLimit(t *testing.T) {
	srv := chainServer(3)
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true, FollowRedirects: 2})
	targets := []unsub.Target{{URIs: []string{srv.URL + "/step/0"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
}

func TestHTTPGetDisabledIsManual(t *testing.T) {
	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: false})
	targets := []unsub.Target{{URIs: []string{"https://example.invalid/unsub"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusManual {
		t.Fatalf("status = %q, want manual", r.Status)
	}
	if r.Err != "http_get disabled" {
		t.Fatalf("err = %q, want %q", r.Err, "http_get disabled")
	}
}

func TestMailtoNoSMTPIsManual(t *testing.T) {
	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{URIs: []string{"mailto:list@example.com"}}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusManual {
		t.Fatalf("status = %q, want manual", r.Status)
	}
	if r.Err != "smtp not configured" {
		t.Fatalf("err = %q, want %q", r.Err, "smtp not configured")
	}
	if r.URI != "" {
		t.Fatalf("URI = %q, want empty for manual", r.URI)
	}
}

type sentMail struct {
	to, subject, body string
}

func TestMailtoSendsWithDefaultsAndDecodesSubject(t *testing.T) {
	var mu sync.Mutex
	var calls []sentMail
	fake := func(ctx context.Context, s unsub.SMTP, to, subject, body string) error {
		mu.Lock()
		calls = append(calls, sentMail{to, subject, body})
		mu.Unlock()
		return nil
	}

	e := unsub.New(unsub.Options{AllowPrivateHosts: true,
		SMTP:     &unsub.SMTP{Host: "smtp.example.com", Port: 587},
		SendMail: fake,
	})
	targets := []unsub.Target{
		{SenderKey: "no-subject", URIs: []string{"mailto:a@example.com"}},
		{SenderKey: "with-subject", URIs: []string{"mailto:b@example.com?subject=Please%20remove"}},
	}
	results := e.Run(context.Background(), targets, nil)

	for _, r := range results {
		if r.Status != unsub.StatusOK {
			t.Fatalf("target %s: status = %q, want ok (err=%q)", r.SenderKey, r.Status, r.Err)
		}
	}

	if len(calls) != 2 {
		t.Fatalf("got %d SendMail calls, want 2", len(calls))
	}
	byTo := map[string]sentMail{}
	for _, c := range calls {
		byTo[c.to] = c
	}
	if got := byTo["a@example.com"].subject; got != "unsubscribe" {
		t.Fatalf("default subject = %q, want %q", got, "unsubscribe")
	}
	if got := byTo["b@example.com"].subject; got != "Please remove" {
		t.Fatalf("decoded subject = %q, want %q", got, "Please remove")
	}
}

func TestMethodFallbackOneClickToMailto(t *testing.T) {
	var mu sync.Mutex
	var called bool
	fake := func(ctx context.Context, s unsub.SMTP, to, subject, body string) error {
		mu.Lock()
		called = true
		mu.Unlock()
		return nil
	}

	e := unsub.New(unsub.Options{AllowPrivateHosts: true,
		SMTP:     &unsub.SMTP{Host: "smtp.example.com", Port: 587},
		SendMail: fake,
	})
	targets := []unsub.Target{{
		OneClick: true, // no http(s) URI available, so this cannot be honored
		URIs:     []string{"mailto:list@example.com"},
	}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Method != headers.MethodMailto {
		t.Fatalf("method = %q, want mailto", r.Method)
	}
	if r.Status != unsub.StatusOK {
		t.Fatalf("status = %q, want ok", r.Status)
	}
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatalf("fake SendMail was not called")
	}
}

func TestNoURIsIsManual(t *testing.T) {
	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{SenderKey: "empty"}}
	results := e.Run(context.Background(), targets, nil)

	r := results[0]
	if r.Status != unsub.StatusManual {
		t.Fatalf("status = %q, want manual", r.Status)
	}
	if r.Method != headers.MethodNone {
		t.Fatalf("method = %q, want none", r.Method)
	}
}

// TestRateLimitSameHost checks pacing on one host. Rate limits key on
// hostname with the port stripped, except for IP-literal hosts (see
// hostKey's doc comment in unsub.go) where host:port is kept so that
// httptest servers on 127.0.0.1 with different ports do not collide. All
// three targets below share one server (one host:port), so they must be
// paced.
func TestRateLimitSameHost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true, FollowRedirects: 0, PerHostRPS: 10, MaxInFlight: 4})
	targets := []unsub.Target{
		{URIs: []string{srv.URL + "/a"}},
		{URIs: []string{srv.URL + "/b"}},
		{URIs: []string{srv.URL + "/c"}},
	}

	start := time.Now()
	e.Run(context.Background(), targets, nil)
	elapsed := time.Since(start)

	if elapsed < 200*time.Millisecond {
		t.Fatalf("elapsed = %v, want at least 200ms for 3 requests at 10 rps to one host", elapsed)
	}
}

// TestRateLimitDifferentHosts uses three distinct httptest servers (each
// 127.0.0.1 with its own port). Because host is an IP literal, hostKey
// keeps the port, so these three count as different hosts and are not
// paced against each other.
func TestRateLimitDifferentHosts(t *testing.T) {
	var servers []*httptest.Server
	for i := 0; i < 3; i++ {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		servers = append(servers, srv)
		defer srv.Close()
	}

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true, FollowRedirects: 0, PerHostRPS: 1, MaxInFlight: 4})
	var targets []unsub.Target
	for _, srv := range servers {
		targets = append(targets, unsub.Target{URIs: []string{srv.URL + "/x"}})
	}

	start := time.Now()
	results := e.Run(context.Background(), targets, nil)
	elapsed := time.Since(start)

	for _, r := range results {
		if r.Status != unsub.StatusProbable {
			t.Fatalf("status = %q, want probable (err=%q)", r.Status, r.Err)
		}
	}
	if elapsed >= 200*time.Millisecond {
		t.Fatalf("elapsed = %v, want well under 200ms across 3 different hosts", elapsed)
	}
}

func TestContextCanceledBeforeRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	e := unsub.New(unsub.Options{AllowPrivateHosts: true, HTTPGet: true})
	targets := []unsub.Target{
		{URIs: []string{"https://example.invalid/a"}},
		{URIs: []string{"mailto:someone@example.com"}},
	}

	start := time.Now()
	results := e.Run(ctx, targets, nil)
	elapsed := time.Since(start)

	if elapsed >= 100*time.Millisecond {
		t.Fatalf("elapsed = %v, want a fast failure on an already-canceled context", elapsed)
	}
	for _, r := range results {
		if r.Status != unsub.StatusFailed {
			t.Fatalf("status = %q, want failed", r.Status)
		}
		if r.Err != context.Canceled.Error() {
			t.Fatalf("err = %q, want %q", r.Err, context.Canceled.Error())
		}
	}
}

func TestOnResultCalledPerTarget(t *testing.T) {
	var mu sync.Mutex
	var count int
	e := unsub.New(unsub.Options{AllowPrivateHosts: true})
	targets := []unsub.Target{{SenderKey: "a"}, {SenderKey: "b"}, {SenderKey: "c"}}
	results := e.Run(context.Background(), targets, func(r unsub.Result) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	if count != 3 {
		t.Fatalf("onResult called %d times, want 3", count)
	}
	if len(results) != 3 || results[0].SenderKey != "a" || results[1].SenderKey != "b" || results[2].SenderKey != "c" {
		t.Fatalf("results out of order: %+v", results)
	}
}

// fakeSMTP is a minimal SMTP server that records the DATA payload.
func fakeSMTP(t *testing.T) (host string, port int, data *strings.Builder, done *sync.WaitGroup) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var b strings.Builder
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := bufio.NewWriter(c)
		reply := func(s string) { w.WriteString(s + "\r\n"); w.Flush() }
		reply("220 fake ESMTP")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if strings.TrimRight(line, "\r\n") == "." {
					inData = false
					reply("250 ok")
					continue
				}
				b.WriteString(line)
				continue
			}
			switch up := strings.ToUpper(strings.TrimSpace(line)); {
			case strings.HasPrefix(up, "EHLO"):
				reply("250-fake")
				reply("250 SIZE 100000")
			case strings.HasPrefix(up, "DATA"):
				inData = true
				reply("354 go ahead")
			case strings.HasPrefix(up, "QUIT"):
				reply("221 bye")
				return
			default:
				reply("250 ok")
			}
		}
	}()

	h, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	return h, p, &b, &wg
}

// TestMailtoSubjectCannotInjectHeaders: a List-Unsubscribe mailto: is written
// by the sender, so CRLF in its subject must not become extra headers.
func TestMailtoSubjectCannotInjectHeaders(t *testing.T) {
	host, port, data, wg := fakeSMTP(t)

	uri := "mailto:list@example.com?subject=unsub%0D%0ABcc:%20victim@example.org%0D%0AX-Injected:%20yes" +
		"&body=hi%0D%0A.%0D%0AQUIT%0D%0AX-Body-Injected:%20yes"
	e := unsub.New(unsub.Options{SMTP: &unsub.SMTP{Host: host, Port: port, From: "me@example.com"}})
	results := e.Run(context.Background(), []unsub.Target{{SenderKey: "k", URIs: []string{uri}}}, nil)
	wg.Wait()

	if results[0].Status != unsub.StatusOK {
		t.Fatalf("status = %q (err=%q), want ok", results[0].Status, results[0].Err)
	}
	payload := data.String()
	head, body, ok := strings.Cut(payload, "\r\n\r\n")
	if !ok {
		t.Fatalf("no header/body separator in:\n%s", payload)
	}
	// The injected text survives only as subject characters on the Subject
	// line: no Bcc, no X-Injected, and nothing from the body among the headers.
	for _, line := range strings.Split(head, "\r\n") {
		name, _, _ := strings.Cut(line, ":")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "bcc", "x-injected", "x-body-injected":
			t.Errorf("injected header %q reached the wire:\n%s", line, head)
		}
	}
	if !strings.Contains(head, "Subject: unsubBcc: victim@example.orgX-Injected: yes\r\n") {
		t.Errorf("subject not flattened onto a single header line:\n%s", head)
	}
	// The body keeps its content: the lone "." is dot-stuffed, so the QUIT
	// line stays body text and never becomes an SMTP command.
	if !strings.Contains(body, "QUIT") {
		t.Errorf("body lost its content:\n%s", body)
	}
}

// TestPrivateHostRefusedByDefault: unsubscribe URLs come from the sender, so
// they may not reach the local network unless the user opts in.
func TestPrivateHostRefusedByDefault(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name   string
		target unsub.Target
	}{
		{"one-click", unsub.Target{SenderKey: "a", OneClick: true, URIs: []string{srv.URL + "/u"}}},
		{"get", unsub.Target{SenderKey: "b", URIs: []string{srv.URL + "/u"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := unsub.New(unsub.Options{HTTPGet: true, PerHostRPS: 1000, Client: srv.Client()})
			r := e.Run(context.Background(), []unsub.Target{tc.target}, nil)[0]
			if r.Status != unsub.StatusFailed {
				t.Fatalf("status = %q, want failed", r.Status)
			}
			if !strings.Contains(r.Err, "private address") ||
				!strings.Contains(r.Err, "allow_private_hosts") {
				t.Fatalf("err = %q, want the private-address refusal", r.Err)
			}
		})
	}
	if n := atomic.LoadInt32(&hits); n != 0 {
		t.Fatalf("server was reached %d time(s) despite the refusal", n)
	}
}

func TestPrivateHostAllowedWithFlag(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	e := unsub.New(unsub.Options{
		HTTPGet: true, PerHostRPS: 1000, Client: srv.Client(), AllowPrivateHosts: true,
	})
	r := e.Run(context.Background(), []unsub.Target{
		{SenderKey: "a", OneClick: true, URIs: []string{srv.URL + "/u"}},
	}, nil)[0]
	if r.Status != unsub.StatusOK {
		t.Fatalf("status = %q (err=%q), want ok", r.Status, r.Err)
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("server reached %d time(s), want 1", n)
	}
}

// TestPrivateHostRedirectTargetNeverReached: with the guard on, a redirect
// chain reaches nothing. The entry point is refused first here, since an
// offline test cannot host the first hop on a public address; the per-hop
// check in CheckRedirect is the same function.
func TestPrivateHostRedirectTargetNeverReached(t *testing.T) {
	var inner int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&inner, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/u", http.StatusFound)
	}))
	defer redirector.Close()

	e := unsub.New(unsub.Options{
		HTTPGet: true, FollowRedirects: 5, PerHostRPS: 1000, Client: redirector.Client(),
	})
	r := e.Run(context.Background(), []unsub.Target{
		{SenderKey: "a", URIs: []string{redirector.URL + "/u"}},
	}, nil)[0]
	if r.Status != unsub.StatusFailed {
		t.Fatalf("status = %q, want failed", r.Status)
	}
	if n := atomic.LoadInt32(&inner); n != 0 {
		t.Fatalf("redirect target was reached %d time(s)", n)
	}
}

func TestOneClickRedirectFallsBackToGet(t *testing.T) {
	var posts, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			posts++
			http.Redirect(w, r, "/done", http.StatusFound)
		default:
			gets++
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()
	e := unsub.New(unsub.Options{HTTPGet: true, FollowRedirects: 3, AllowPrivateHosts: true})
	res := e.Run(context.Background(), []unsub.Target{{SenderKey: "k", URIs: []string{srv.URL + "/u"}, OneClick: true}}, nil)
	if res[0].Status != unsub.StatusProbable || res[0].Method != headers.MethodHTTP || posts != 1 || gets == 0 {
		t.Fatalf("got %+v posts=%d gets=%d", res[0], posts, gets)
	}
}

func TestDescribeNetError(t *testing.T) {
	dns := unsub.DescribeNetError(&net.DNSError{Err: "no such host", Name: "e.example.com"})
	if !strings.Contains(dns, "e.example.com") || !strings.Contains(dns, "DNS filter") {
		t.Fatalf("dns: %q", dns)
	}
	e := unsub.New(unsub.Options{HTTPGet: true, AllowPrivateHosts: true, Timeout: time.Second})
	res := e.Run(context.Background(), []unsub.Target{{SenderKey: "k", URIs: []string{"http://127.0.0.1:9/u"}, OneClick: true}}, nil)
	if res[0].Status != unsub.StatusFailed || !strings.Contains(res[0].Err, "refused or unreachable") {
		t.Fatalf("dial: %+v", res[0])
	}
}
