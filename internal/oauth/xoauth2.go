package oauth

import (
	"errors"
	"net/smtp"

	"github.com/emersion/go-sasl"
)

// Mechanism is the SASL mechanism name Google and Microsoft accept for IMAP
// and SMTP. It predates OAUTHBEARER (RFC 7628) and neither provider has
// retired it.
const Mechanism = "XOAUTH2"

// XOAUTH2 builds the mechanism's initial client response:
//
//	user=<username>\x01auth=Bearer <access token>\x01\x01
//
// The caller base64-encodes it; go-imap and net/smtp both do that
// themselves.
func XOAUTH2(user, accessToken string) []byte {
	return []byte("user=" + user + "\x01auth=Bearer " + accessToken + "\x01\x01")
}

// ChallengeError is the server's XOAUTH2 failure challenge: a small JSON
// document naming why the token was refused. Surfacing it verbatim is the
// difference between "AUTHENTICATE failed" and a reason the user can act on.
type ChallengeError struct {
	Text string
}

func (e *ChallengeError) Error() string {
	return "xoauth2: the server rejected the token: " + e.Text
}

// saslClient is the single-shot IMAP side of the mechanism.
type saslClient struct {
	user  string
	token string
	// challenge records the failure document the server sent, sanitized. The
	// exchange carries on after it, so this is read once the server's NO has
	// come back through Authenticate.
	challenge string
}

func (a *saslClient) Start() (string, []byte, error) {
	return Mechanism, XOAUTH2(a.user, a.token), nil
}

// Next only ever runs when the server refused the token: success ends the
// exchange without a challenge. The mechanism wants an empty client response
// here and then sends its NO, so returning an error instead would abandon the
// exchange mid-command and leave the connection out of step. The challenge is
// kept for the caller to attach to the error the NO produces.
func (a *saslClient) Next(challenge []byte) ([]byte, error) {
	a.challenge = sanitize(string(challenge))
	return []byte{}, nil
}

// SASLClient returns the go-sasl client go-imap's Authenticate takes, plus a
// function reporting the failure challenge the server sent, empty when there
// was none. Call it after Authenticate returns.
func SASLClient(user, accessToken string) (sasl.Client, func() string) {
	c := &saslClient{user: user, token: accessToken}
	return c, func() string { return c.challenge }
}

// smtpAuth is the net/smtp side of the same mechanism, for smtp.gmail.com and
// smtp.office365.com.
type smtpAuth struct {
	user  string
	token string
}

// Start refuses a connection that is not encrypted: the bearer token is as
// good as the mailbox, so it never travels in the clear. net/smtp calls this
// after STARTTLS, so a provider that offers TLS at all passes.
func (a *smtpAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server == nil || !server.TLS {
		return "", nil, errors.New("xoauth2: refusing to send an access token over an unencrypted connection")
	}
	return Mechanism, XOAUTH2(a.user, a.token), nil
}

func (a *smtpAuth) Next(fromServer []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	return nil, &ChallengeError{Text: sanitize(string(fromServer))}
}

// SMTPAuth returns the net/smtp Auth for XOAUTH2.
func SMTPAuth(user, accessToken string) smtp.Auth {
	return &smtpAuth{user: user, token: accessToken}
}
