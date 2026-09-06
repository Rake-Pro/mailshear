// Package imapx wraps emersion/go-imap/v2 with the operations mailshear needs.
//
// TODO(gmail): go-imap/v2 v2.0.0-beta.8 has no support for the Gmail IMAP
// extension X-GM-EXT-1. imap.FetchOptions cannot request X-GM-MSGID or
// X-GM-LABELS, imap.SearchCriteria cannot express X-GM-RAW, and imapclient
// exposes no way to send a raw command (beginCommand is unexported). As a
// result Header.GmMsgID and Header.GmLabels are always zero values even when
// Client.Gmail is true, and SearchGmailRaw always fails. Revisit when the
// library gains the extension or a raw-command escape hatch.
package imapx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/rs/zerolog/log"

	"github.com/Rake-Pro/mailshear/internal/headers"
	"github.com/Rake-Pro/mailshear/internal/oauth"
)

// CapGmail is the Gmail extension capability.
const CapGmail imap.Cap = "X-GM-EXT-1"

var headerFields = []string{
	"FROM",
	"SENDER",
	"LIST-ID",
	"LIST-UNSUBSCRIBE",
	"LIST-UNSUBSCRIBE-POST",
	"PRECEDENCE",
	"SUBJECT",
	"MESSAGE-ID",
	"DATE",
}

type Client struct {
	c *imapclient.Client

	Caps       imap.CapSet
	Gmail      bool
	HasMove    bool
	HasUIDPlus bool
}

type MailboxInfo struct {
	Name        string
	UIDValidity uint32
	UIDNext     uint32
}

type Header struct {
	UID          uint32
	Flags        []string
	InternalDate time.Time
	Size         int64
	Fields       map[string][]string

	GmMsgID  uint64
	GmLabels []string
}

func Dial(ctx context.Context, host string, port int, username, password string) (*Client, error) {
	ic, err := connect(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return login(ic, host, username, password)
}

// DialXOAUTH2 connects the same way as Dial and authenticates with an OAuth
// 2.0 access token over SASL XOAUTH2, which is what Gmail, Workspace and
// Microsoft 365 accept in place of an app password.
func DialXOAUTH2(ctx context.Context, host string, port int, username, accessToken string) (*Client, error) {
	ic, err := connect(ctx, host, port)
	if err != nil {
		return nil, err
	}
	return authXOAUTH2(ic, host, username, accessToken)
}

// connect opens the TLS connection both dial paths share.
func connect(ctx context.Context, host string, port int) (*imapclient.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ic, err := imapclient.DialTLS(addr, &imapclient.Options{
		TLSConfig: &tls.Config{ServerName: host},
	})
	if err != nil {
		return nil, fmt.Errorf("imap: dial %s: %w", addr, err)
	}
	return ic, nil
}

// dialInsecure connects without TLS. Test-only.
func dialInsecure(addr, username, password string) (*Client, error) {
	ic, host, err := connectInsecure(addr)
	if err != nil {
		return nil, err
	}
	return login(ic, host, username, password)
}

// dialInsecureXOAUTH2 connects without TLS and authenticates with a token.
// Test-only: a real token never travels unencrypted.
func dialInsecureXOAUTH2(addr, username, accessToken string) (*Client, error) {
	ic, host, err := connectInsecure(addr)
	if err != nil {
		return nil, err
	}
	return authXOAUTH2(ic, host, username, accessToken)
}

func connectInsecure(addr string) (*imapclient.Client, string, error) {
	ic, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		return nil, "", fmt.Errorf("imap: dial %s: %w", addr, err)
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		host = addr
	}
	return ic, host, nil
}

func login(ic *imapclient.Client, host, username, password string) (*Client, error) {
	if err := ic.Login(username, password).Wait(); err != nil {
		ic.Close()
		return nil, loginError(host, err)
	}
	return newClient(ic), nil
}

// authXOAUTH2 runs AUTHENTICATE XOAUTH2. A server that does not advertise the
// mechanism is refused before the token is sent, so the account owner is told
// what is wrong rather than watching an opaque AUTHENTICATE failure.
func authXOAUTH2(ic *imapclient.Client, host, username, accessToken string) (*Client, error) {
	if !ic.Caps().Has(imap.AuthCap(oauth.Mechanism)) {
		ic.Close()
		return nil, fmt.Errorf("imap: %s does not advertise AUTH=XOAUTH2; this account cannot use OAuth, set auth: file and store an app password", host)
	}
	sc, challenge := oauth.SASLClient(username, accessToken)
	if err := ic.Authenticate(sc); err != nil {
		ic.Close()
		return nil, xoauth2Error(host, err, challenge())
	}
	return newClient(ic), nil
}

func newClient(ic *imapclient.Client) *Client {
	caps := ic.Caps()
	return &Client{
		c:          ic,
		Caps:       caps,
		Gmail:      caps.Has(CapGmail),
		HasMove:    caps.Has(imap.CapMove),
		HasUIDPlus: caps.Has(imap.CapUIDPlus),
	}
}

var gmailLoginMarkers = []string{
	"application-specific password required",
	"invalid credentials",
	"[alert]",
	"https://support.google.com",
}

const gmailLoginHint = "Gmail rejected the login. Use an app password from https://myaccount.google.com/apppasswords (requires 2-Step Verification) and make sure IMAP is enabled in Gmail settings; Workspace admins can disable both."

func loginError(host string, err error) error {
	if isGoogleHost(host) {
		msg := strings.ToLower(err.Error())
		for _, m := range gmailLoginMarkers {
			if strings.Contains(msg, m) {
				return fmt.Errorf("imap: login failed: %w: %s", err, gmailLoginHint)
			}
		}
	}
	return fmt.Errorf("imap: login failed: %w", err)
}

// gmailOAuthHint says the one thing a rejected Google token usually means.
// Testing-mode OAuth clients have their refresh tokens expired by Google
// after seven days, which looks like a working setup breaking for no reason.
const gmailOAuthHint = "the token was rejected; sign in again from the accounts screen (Google testing-mode OAuth clients expire refresh tokens after 7 days)"

// xoauth2Error reports a refused token. The server answers a bad token with a
// base64 challenge naming the reason and only then a bare NO, so the challenge
// is what turns "AUTHENTICATE failed" into something actionable; it is already
// sanitized by the SASL client.
func xoauth2Error(host string, err error, challenge string) error {
	out := fmt.Errorf("imap: xoauth2 login failed: %w", err)
	if challenge != "" {
		out = fmt.Errorf("%w: server said %s", out, challenge)
	}
	if isGoogleHost(host) {
		return fmt.Errorf("%w: %s", out, gmailOAuthHint)
	}
	return out
}

func isGoogleHost(host string) bool {
	h := strings.ToLower(strings.TrimSuffix(host, "."))
	for _, d := range []string{"gmail.com", "googlemail.com", "google.com"} {
		if h == d || strings.HasSuffix(h, "."+d) {
			return true
		}
	}
	return false
}

func (c *Client) Close() error {
	c.c.Logout().Wait()
	return c.c.Close()
}

func (c *Client) SelectReadOnly(ctx context.Context, name string) (MailboxInfo, error) {
	if err := ctx.Err(); err != nil {
		return MailboxInfo{}, err
	}
	data, err := c.c.Select(name, &imap.SelectOptions{ReadOnly: true}).Wait()
	if err != nil {
		return MailboxInfo{}, fmt.Errorf("imap: examine %q: %w", name, err)
	}
	return MailboxInfo{
		Name:        name,
		UIDValidity: data.UIDValidity,
		UIDNext:     uint32(data.UIDNext),
	}, nil
}

func (c *Client) FindSpecialUse(ctx context.Context, attr imap.MailboxAttr) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	cmd := c.c.List("", "*", &imap.ListOptions{ReturnSpecialUse: true})
	found := ""
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		if found != "" {
			continue
		}
		for _, a := range data.Attrs {
			if a == attr {
				found = data.Mailbox
				break
			}
		}
	}
	if err := cmd.Close(); err != nil {
		return "", fmt.Errorf("imap: list special-use %s: %w", attr, err)
	}
	return found, nil
}

func (c *Client) FetchHeaders(ctx context.Context, lo, hi uint32, fn func(Header) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var set imap.UIDSet
	set.AddRange(imap.UID(lo), imap.UID(hi))

	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: headerFields,
		Peek:         true,
	}
	cmd := c.c.Fetch(set, &imap.FetchOptions{
		UID:          true,
		Flags:        true,
		InternalDate: true,
		RFC822Size:   true,
		BodySection:  []*imap.FetchItemBodySection{section},
	})

	var cbErr error
	for {
		msg := cmd.Next()
		if msg == nil {
			break
		}
		if cbErr != nil || ctx.Err() != nil {
			continue
		}
		buf, err := msg.Collect()
		if err != nil {
			cbErr = fmt.Errorf("imap: fetch headers: %w", err)
			continue
		}
		h, err := headerFromBuffer(buf)
		if err != nil {
			// One unparseable header block must not end the folder sweep.
			// The error text can quote the offending header, so it is not
			// logged.
			log.Warn().Uint32("uid", h.UID).Msg("skipping message with unparseable headers")
			continue
		}
		if err := fn(h); err != nil {
			cbErr = err
		}
	}
	if err := cmd.Close(); err != nil && cbErr == nil {
		return fmt.Errorf("imap: fetch headers: %w", err)
	}
	if cbErr != nil {
		return cbErr
	}
	return ctx.Err()
}

// headerFromBuffer converts one fetched message into a Header. An unparseable
// header block is reported as an error so the caller can skip the message.
func headerFromBuffer(buf *imapclient.FetchMessageBuffer) (Header, error) {
	h := Header{
		UID:          uint32(buf.UID),
		InternalDate: buf.InternalDate,
		Size:         buf.RFC822Size,
		Fields:       map[string][]string{},
	}
	for _, f := range buf.Flags {
		h.Flags = append(h.Flags, string(f))
	}
	for _, bs := range buf.BodySection {
		fields, err := parseHeaderFields(bs.Bytes)
		if err != nil {
			return h, fmt.Errorf("imap: parse headers: %w", err)
		}
		for k, v := range fields {
			h.Fields[k] = append(h.Fields[k], v...)
		}
	}
	return h, nil
}

func parseHeaderFields(b []byte) (map[string][]string, error) {
	if len(b) == 0 {
		return nil, nil
	}
	if !bytes.HasSuffix(b, []byte("\n\n")) && !bytes.HasSuffix(b, []byte("\n\r\n")) {
		b = append(append([]byte(nil), b...), "\r\n"...)
	}
	r := textproto.NewReader(bufio.NewReader(bytes.NewReader(b)))
	h, err := r.ReadMIMEHeader()
	if err != nil && !errors.Is(err, io.EOF) {
		return h, err
	}
	return h, nil
}

func (c *Client) SearchGmailRaw(ctx context.Context, query string, lo, hi uint32) ([]uint32, error) {
	return nil, errors.New("gmail prefilter not supported by the IMAP library")
}

const (
	// fetchBatch caps how many UIDs go into one UID FETCH.
	fetchBatch = 500
	// moveBatch caps how many UIDs go into one UID MOVE/COPY/STORE/EXPUNGE.
	moveBatch = 200
)

// Select opens a mailbox read-write. Use SelectReadOnly for anything that
// must not change server state.
func (c *Client) Select(ctx context.Context, name string) (MailboxInfo, error) {
	if err := ctx.Err(); err != nil {
		return MailboxInfo{}, err
	}
	data, err := c.c.Select(name, nil).Wait()
	if err != nil {
		return MailboxInfo{}, fmt.Errorf("imap: select %q: %w", name, err)
	}
	return MailboxInfo{
		Name:        name,
		UIDValidity: data.UIDValidity,
		UIDNext:     uint32(data.UIDNext),
	}, nil
}

// chunkUIDs splits uids into batches of at most size.
func chunkUIDs(uids []uint32, size int) [][]uint32 {
	if size <= 0 {
		size = 1
	}
	var out [][]uint32
	for i := 0; i < len(uids); i += size {
		hi := i + size
		if hi > len(uids) {
			hi = len(uids)
		}
		out = append(out, uids[i:hi])
	}
	return out
}

func uidSetOf(uids []uint32) imap.UIDSet {
	var set imap.UIDSet
	for _, u := range uids {
		set.AddNum(imap.UID(u))
	}
	return set
}

// fetchBatches runs one UID FETCH per batch of at most fetchBatch UIDs,
// calling fn for every message that came back whole.
func (c *Client) fetchBatches(ctx context.Context, uids []uint32, opts *imap.FetchOptions, what string, fn func(*imapclient.FetchMessageBuffer)) error {
	for _, batch := range chunkUIDs(uids, fetchBatch) {
		if err := ctx.Err(); err != nil {
			return err
		}
		cmd := c.c.Fetch(uidSetOf(batch), opts)
		var collectErr error
		for {
			msg := cmd.Next()
			if msg == nil {
				break
			}
			buf, err := msg.Collect()
			if err != nil {
				if collectErr == nil {
					collectErr = err
				}
				continue
			}
			fn(buf)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("imap: fetch %s: %w", what, err)
		}
		if collectErr != nil {
			return fmt.Errorf("imap: fetch %s: %w", what, collectErr)
		}
	}
	return nil
}

// FetchFlags returns the current flags for uids. Messages the server no
// longer has are simply absent from the map.
func (c *Client) FetchFlags(ctx context.Context, uids []uint32) (map[uint32][]string, error) {
	out := make(map[uint32][]string, len(uids))
	err := c.fetchBatches(ctx, uids, &imap.FetchOptions{UID: true, Flags: true}, "flags",
		func(buf *imapclient.FetchMessageBuffer) {
			flags := make([]string, 0, len(buf.Flags))
			for _, f := range buf.Flags {
				flags = append(flags, string(f))
			}
			out[uint32(buf.UID)] = flags
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Identity is what a message says about itself at apply time: the identity
// check before a move, and the subject the keep rule is evaluated against.
type Identity struct {
	// MessageID has the angle brackets trimmed, so it compares directly
	// against what scan stored.
	MessageID string
	// Subject is RFC 2047 decoded.
	Subject string
}

// FetchIdentity returns the Message-ID and Subject of uids in one round trip.
// Messages the server no longer has are simply absent from the map. BODY.PEEK
// keeps the fetch from setting \Seen.
func (c *Client) FetchIdentity(ctx context.Context, uids []uint32) (map[uint32]Identity, error) {
	section := &imap.FetchItemBodySection{
		Specifier:    imap.PartSpecifierHeader,
		HeaderFields: []string{"MESSAGE-ID", "SUBJECT"},
		Peek:         true,
	}
	opts := &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}

	out := make(map[uint32]Identity, len(uids))
	err := c.fetchBatches(ctx, uids, opts, "identity", func(buf *imapclient.FetchMessageBuffer) {
		for _, bs := range buf.BodySection {
			fields, err := parseHeaderFields(bs.Bytes)
			if err != nil {
				continue
			}
			id := out[uint32(buf.UID)]
			if v := fields["Message-Id"]; len(v) > 0 {
				id.MessageID = headers.TrimAngles(v[0])
			}
			if v := fields["Subject"]; len(v) > 0 {
				id.Subject = headers.DecodeSubject(v[0])
			}
			out[uint32(buf.UID)] = id
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// MoveUIDs moves uids out of the selected mailbox into dest, in batches of
// at most moveBatch. UID MOVE is used when the server advertises MOVE. The
// fallback is UID COPY, +FLAGS.SILENT (\Deleted) and UID EXPUNGE, which
// needs UIDPLUS so that only the copied messages are expunged: without it
// a plain EXPUNGE would remove every \Deleted message in the mailbox, so
// this refuses to act instead.
func (c *Client) MoveUIDs(ctx context.Context, uids []uint32, dest string) error {
	if len(uids) == 0 {
		return nil
	}
	if !c.HasMove && !c.HasUIDPlus {
		return errors.New("server supports neither MOVE nor UIDPLUS; refusing to expunge")
	}
	for _, batch := range chunkUIDs(uids, moveBatch) {
		if err := ctx.Err(); err != nil {
			return err
		}
		set := uidSetOf(batch)
		if c.HasMove {
			if _, err := c.c.Move(set, dest).Wait(); err != nil {
				return fmt.Errorf("imap: move %d uid(s) to %q: %w", len(batch), dest, err)
			}
			continue
		}
		if _, err := c.c.Copy(set, dest).Wait(); err != nil {
			return fmt.Errorf("imap: copy %d uid(s) to %q: %w", len(batch), dest, err)
		}
		flagCmd := c.c.Store(set, &imap.StoreFlags{
			Op:     imap.StoreFlagsAdd,
			Silent: true,
			Flags:  []imap.Flag{imap.FlagDeleted},
		}, nil)
		if err := flagCmd.Close(); err != nil {
			return fmt.Errorf("imap: flag %d uid(s) deleted: %w", len(batch), err)
		}
		if _, err := c.c.UIDExpunge(set).Collect(); err != nil {
			return fmt.Errorf("imap: uid expunge %d uid(s): %w", len(batch), err)
		}
	}
	return nil
}

// SearchMessageID returns the UIDs in the selected mailbox whose Message-ID
// header matches. UIDs change when a message moves, so undo locates messages
// by this stable identity rather than by the UID recorded at apply time.
func (c *Client) SearchMessageID(ctx context.Context, messageID string) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := headers.TrimAngles(messageID)
	if id == "" {
		return nil, errors.New("imap: search message-id: empty id")
	}
	data, err := c.c.UIDSearch(&imap.SearchCriteria{
		Header: []imap.SearchCriteriaHeaderField{{Key: "Message-Id", Value: "<" + id + ">"}},
	}, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: search message-id: %w", err)
	}
	uids := data.AllUIDs()
	out := make([]uint32, 0, len(uids))
	for _, u := range uids {
		out = append(out, uint32(u))
	}
	return out, nil
}

// ListMailboxes returns every mailbox name the account can see.
func (c *Client) ListMailboxes(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := c.c.List("", "*", nil)
	var out []string
	for {
		data := cmd.Next()
		if data == nil {
			break
		}
		out = append(out, data.Mailbox)
	}
	if err := cmd.Close(); err != nil {
		return nil, fmt.Errorf("imap: list mailboxes: %w", err)
	}
	return out, nil
}

// DialInsecureForTest connects without TLS. It exists so sibling packages can
// exercise their IMAP paths against an in-memory server; production code
// always goes through Dial.
func DialInsecureForTest(addr, username, password string) (*Client, error) {
	return dialInsecure(addr, username, password)
}
