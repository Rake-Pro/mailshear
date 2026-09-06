package oauth

import (
	"errors"
	"net/smtp"
	"testing"
)

func TestXOAUTH2ByteLayout(t *testing.T) {
	got := string(XOAUTH2("me@example.com", "at-1"))
	want := "user=me@example.com\x01auth=Bearer at-1\x01\x01"
	if got != want {
		t.Fatalf("XOAUTH2 = %q, want %q", got, want)
	}
}

func TestSASLClientStartAndChallenge(t *testing.T) {
	c, challengeText := SASLClient("me@example.com", "at-1")
	mech, ir, err := c.Start()
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mech != "XOAUTH2" {
		t.Fatalf("mechanism = %q", mech)
	}
	if string(ir) != string(XOAUTH2("me@example.com", "at-1")) {
		t.Fatalf("initial response = %q", ir)
	}
	if challengeText() != "" {
		t.Fatalf("challenge before one arrived = %q", challengeText())
	}

	// The mechanism wants an empty response here; the server then sends its
	// NO, which is what the caller reports. Returning an error instead would
	// abandon the exchange half way through the command.
	const challenge = `{"status":"401","schemes":"Bearer","scope":"https://mail.google.com/"}`
	resp, err := c.Next([]byte(challenge))
	if err != nil {
		t.Fatalf("Next returned an error instead of an empty response: %v", err)
	}
	if resp == nil || len(resp) != 0 {
		t.Fatalf("response = %q, want an empty (non-nil) response", resp)
	}
	if challengeText() != challenge {
		t.Fatalf("recorded challenge = %q, want %q", challengeText(), challenge)
	}
}

// A challenge is a server-supplied string that ends up printed, so the escape
// sequences a hostile or compromised server could put in it are removed
// before it is recorded.
func TestSASLClientSanitizesTheChallenge(t *testing.T) {
	c, challengeText := SASLClient("me@example.com", "at-1")
	if _, err := c.Next([]byte("bad\x1b]0;pwned\x07 token\x1b[31m")); err != nil {
		t.Fatalf("Next: %v", err)
	}
	if got := challengeText(); got != "bad token" {
		t.Fatalf("challenge = %q, want the escape sequences gone", got)
	}
}

func TestSMTPAuthSanitizesTheChallenge(t *testing.T) {
	a := SMTPAuth("me@example.com", "at-1")
	_, err := a.Next([]byte("\x1b[2Jdenied"), true)
	var ce *ChallengeError
	if !errors.As(err, &ce) {
		t.Fatalf("error = %v, want a ChallengeError", err)
	}
	if ce.Text != "denied" {
		t.Fatalf("challenge = %q, want the escape sequence gone", ce.Text)
	}
}

func TestSMTPAuthStartRequiresTLS(t *testing.T) {
	a := SMTPAuth("me@example.com", "at-1")

	if _, _, err := a.Start(&smtp.ServerInfo{Name: "smtp.gmail.com", TLS: false}); err == nil {
		t.Fatal("Start sent a token over an unencrypted connection")
	}
	if _, _, err := a.Start(nil); err == nil {
		t.Fatal("Start accepted a nil ServerInfo")
	}

	mech, ir, err := a.Start(&smtp.ServerInfo{Name: "smtp.gmail.com", TLS: true})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if mech != "XOAUTH2" {
		t.Fatalf("mechanism = %q", mech)
	}
	if string(ir) != string(XOAUTH2("me@example.com", "at-1")) {
		t.Fatalf("initial response = %q", ir)
	}
}

func TestSMTPAuthNext(t *testing.T) {
	a := SMTPAuth("me@example.com", "at-1")

	// The server ended the exchange: nothing more to send.
	resp, err := a.Next([]byte("2.7.0 Accepted"), false)
	if err != nil || resp != nil {
		t.Fatalf("Next(more=false) = %q, %v", resp, err)
	}

	const challenge = `{"status":"400","schemes":"Bearer"}`
	resp, err = a.Next([]byte(challenge), true)
	if resp != nil {
		t.Fatalf("response = %q, want none", resp)
	}
	var ce *ChallengeError
	if !errors.As(err, &ce) || ce.Text != challenge {
		t.Fatalf("error = %v, want the challenge text", err)
	}
}
