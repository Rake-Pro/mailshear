// Package headers parses RFC 5322/2369/8058 email headers relevant to
// identifying bulk mail and its unsubscribe method. All functions are pure.
package headers

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/mail"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/publicsuffix"
)

type Method string

const (
	MethodOneClick Method = "oneclick"
	MethodHTTP     Method = "http"
	MethodMailto   Method = "mailto"
	MethodNone     Method = "none"
)

type Unsubscribe struct {
	URIs     []string
	HTTPS    []string
	Mailto   []string
	OneClick bool
	Method   Method
}

var wordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

// charsetReader tolerates unknown or legacy charsets (e.g. windows-1252,
// GB2312) by passing the raw bytes through unchanged instead of failing.
// mime.WordDecoder already handles utf-8, us-ascii and iso-8859-1 natively;
// this is only reached for anything else.
func charsetReader(_ string, input io.Reader) (io.Reader, error) {
	return input, nil
}

var foldRe = regexp.MustCompile(`\r\n[ \t]+|\n[ \t]+`)

func unfold(s string) string {
	s = foldRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "\r\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

var bareSplitRe = regexp.MustCompile(`[,\s]+`)

func splitBare(s string) []string {
	var out []string
	for _, tok := range bareSplitRe.Split(s, -1) {
		tok = strings.TrimSpace(tok)
		if tok != "" {
			out = append(out, tok)
		}
	}
	return out
}

var wsRe = regexp.MustCompile(`[ \t\r\n]+`)

// stripWS removes the whitespace a folded header can leave inside an
// angle-bracketed URI, which is never significant there.
func stripWS(s string) string {
	return wsRe.ReplaceAllString(s, "")
}

// validURI rejects the schemes mailshear cannot act on as well as scheme-only
// URIs such as "https://" or "mailto:".
func validURI(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "mailto":
		return u.Opaque != "" || strings.TrimPrefix(u.Path, "/") != ""
	default:
		return false
	}
}

// ParseListUnsubscribe splits a raw List-Unsubscribe value into URIs.
func ParseListUnsubscribe(raw string) []string {
	raw = unfold(raw)

	var candidates []string
	i := 0
	for i < len(raw) {
		if raw[i] == '<' {
			rest := raw[i+1:]
			j := strings.IndexByte(rest, '>')
			if j == -1 {
				candidates = append(candidates, splitBare(rest)...)
				break
			}
			content := stripWS(rest[:j])
			if content != "" {
				candidates = append(candidates, content)
			}
			i = i + 1 + j + 1
			continue
		}
		k := strings.IndexByte(raw[i:], '<')
		var seg string
		if k == -1 {
			seg = raw[i:]
			i = len(raw)
		} else {
			seg = raw[i : i+k]
			i += k
		}
		candidates = append(candidates, splitBare(seg)...)
	}

	seen := make(map[string]bool, len(candidates))
	var out []string
	for _, c := range candidates {
		// A URI carrying a control character is dropped rather than cleaned:
		// what is left after the escape sequence comes out is not the link
		// the sender wrote, and following it is not worth the guess.
		if hasControl(c) {
			continue
		}
		u, err := url.Parse(c)
		if err != nil || !validURI(u) {
			continue
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

// Classify combines List-Unsubscribe and List-Unsubscribe-Post into an
// Unsubscribe. Empty listUnsub yields Method none with no URIs.
func Classify(listUnsub, listUnsubPost string) Unsubscribe {
	uris := ParseListUnsubscribe(listUnsub)
	if len(uris) == 0 {
		return Unsubscribe{Method: MethodNone}
	}

	var https, httpOnly, mailtoURIs []string
	for _, u := range uris {
		parsed, err := url.Parse(u)
		if err != nil {
			continue
		}
		switch strings.ToLower(parsed.Scheme) {
		case "https":
			https = append(https, u)
		case "http":
			httpOnly = append(httpOnly, u)
		case "mailto":
			mailtoURIs = append(mailtoURIs, u)
		}
	}

	var combined []string
	combined = append(combined, https...)
	combined = append(combined, httpOnly...)

	oneClick := len(combined) > 0 && strings.Contains(strings.ToLower(listUnsubPost), "list-unsubscribe=one-click")

	var method Method
	switch {
	case oneClick:
		method = MethodOneClick
	case len(combined) > 0:
		method = MethodHTTP
	case len(mailtoURIs) > 0:
		method = MethodMailto
	default:
		method = MethodNone
	}

	return Unsubscribe{
		URIs:     uris,
		HTTPS:    combined,
		Mailto:   mailtoURIs,
		OneClick: oneClick,
		Method:   method,
	}
}

// ParseListID returns the bracketed id from a List-Id header, lowercased.
func ParseListID(raw string) string {
	raw = strings.TrimSpace(unfold(raw))
	if raw == "" {
		return ""
	}
	if i := strings.IndexByte(raw, '<'); i != -1 {
		if j := strings.IndexByte(raw[i+1:], '>'); j != -1 {
			return Clean(strings.ToLower(strings.TrimSpace(raw[i+1 : i+1+j])))
		}
	}
	return Clean(strings.ToLower(raw))
}

var addrRe = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// ParseFrom returns the RFC 2047-decoded display name and lowercased address
// from a From header. Tolerates bare addresses and malformed input.
func ParseFrom(raw string) (display, address string) {
	raw = strings.TrimSpace(unfold(raw))
	if raw == "" {
		return "", ""
	}

	parser := &mail.AddressParser{WordDecoder: wordDecoder}
	if a, err := parser.Parse(raw); err == nil {
		return Clean(lenientDecode(strings.TrimSpace(a.Name))), Clean(strings.ToLower(strings.TrimSpace(a.Address)))
	}

	m := addrRe.FindString(raw)
	if m == "" {
		return "", ""
	}
	address = strings.ToLower(m)

	displayPart := raw
	if idx := strings.Index(raw, m); idx >= 0 {
		displayPart = raw[:idx]
	}
	displayPart = strings.TrimSpace(displayPart)
	displayPart = strings.Trim(displayPart, "<>")
	displayPart = strings.TrimSpace(displayPart)

	decoded, err := wordDecoder.DecodeHeader(displayPart)
	if err != nil {
		decoded = displayPart
	}
	decoded = strings.Trim(decoded, `"`)
	decoded = strings.TrimSpace(decoded)

	return Clean(lenientDecode(decoded)), Clean(address)
}

// DecodeSubject decodes RFC 2047 words and collapses whitespace.
func DecodeSubject(raw string) string {
	raw = unfold(raw)
	decoded, err := wordDecoder.DecodeHeader(raw)
	if err != nil {
		decoded = raw
	}
	return Clean(lenientDecode(decoded))
}

var encodedWordRe = regexp.MustCompile(`=\?([^?]+)\?([BbQq])\?([^?]*)\?=`)

// lenientDecode handles encoded words the standard decoder leaves alone:
// inside quoted strings, with missing base64 padding, or glued together
// without whitespace. Whitespace between two encoded words is dropped per
// RFC 2047; anything that still fails to decode is kept as is.
func lenientDecode(s string) string {
	if !strings.Contains(s, "=?") {
		return s
	}
	// drop whitespace between adjacent encoded words
	s = regexp.MustCompile(`\?=\s+=\?`).ReplaceAllString(s, "?==?")
	return encodedWordRe.ReplaceAllStringFunc(s, func(w string) string {
		m := encodedWordRe.FindStringSubmatch(w)
		charset, enc, text := strings.ToLower(m[1]), strings.ToUpper(m[2]), m[3]
		var b []byte
		var err error
		if enc == "B" {
			b, err = base64.StdEncoding.DecodeString(text)
			if err != nil {
				b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(text, "="))
			}
		} else {
			b, err = decodeQ(text)
		}
		if err != nil {
			return w
		}
		if charset != "utf-8" && charset != "us-ascii" && charset != "ascii" {
			r, cerr := charsetReader(charset, bytes.NewReader(b))
			if cerr != nil {
				return w
			}
			if out, rerr := io.ReadAll(r); rerr == nil {
				b = out
			}
		}
		return string(b)
	})
}

func decodeQ(text string) ([]byte, error) {
	out := make([]byte, 0, len(text))
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '_':
			out = append(out, ' ')
		case '=':
			if i+2 >= len(text) {
				return nil, fmt.Errorf("truncated Q escape")
			}
			v, err := strconv.ParseUint(text[i+1:i+3], 16, 8)
			if err != nil {
				return nil, err
			}
			out = append(out, byte(v))
			i += 2
		default:
			out = append(out, c)
		}
	}
	return out, nil
}

// SenderKey: listID if non-empty else lowercased fromAddr.
func SenderKey(listID, fromAddr string) string {
	if listID != "" {
		return "list:" + listID
	}
	return "addr:" + strings.ToLower(fromAddr)
}

// DomainKey returns the registrable domain of fromAddr's domain, lowercased.
func DomainKey(fromAddr string) string {
	addr := strings.ToLower(strings.TrimSpace(fromAddr))
	i := strings.LastIndex(addr, "@")
	if i == -1 || i == len(addr)-1 {
		return ""
	}
	domain := strings.TrimSuffix(addr[i+1:], ".")
	if domain == "" {
		return ""
	}
	if strings.HasPrefix(domain, "[") && strings.HasSuffix(domain, "]") {
		return strings.TrimSuffix(strings.TrimPrefix(domain, "["), "]")
	}
	if etld1, err := publicsuffix.EffectiveTLDPlusOne(domain); err == nil {
		return etld1
	}
	return domain
}

// MailtoParts splits a mailto URI into address, subject, body.
func MailtoParts(uri string) (addr, subject, body string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(uri))
	if err != nil || !strings.EqualFold(u.Scheme, "mailto") {
		return "", "", "", false
	}
	addr = u.Opaque
	if addr == "" {
		addr = strings.TrimPrefix(u.Path, "/")
	}
	q := u.Query()
	// The subject becomes a header in the message we send, so anything that
	// could start a new header line is removed rather than escaped. The body
	// keeps its line breaks but is normalized to bare \n; the sender puts the
	// CRLFs back.
	subject = StripControl(q.Get("subject"))
	body = normalizeNewlines(q.Get("body"))
	return strings.ToLower(addr), subject, body, true
}

// StripControl removes every C0 control character and DEL, CR and LF
// included, so a mailto query cannot inject headers.
func StripControl(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// TrimAngles strips one layer of angle brackets and the surrounding space
// from a header value such as a Message-Id.
func TrimAngles(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// normalizeNewlines collapses CRLF and lone CR to \n and drops the other
// control characters, leaving a body that is safe to re-encode as CRLF.
func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// Clean makes a header value safe to print to a terminal. Mail headers are
// attacker-controlled, so an escape sequence in a display name or a subject
// would otherwise be executed by the terminal that renders it: OSC 52 writes
// the clipboard, CSI 2J clears the screen, OSC 0 renames the window. Whole
// escape sequences are removed rather than just their introducer, so nothing
// of them is left to read as text; the remaining C0 controls, DEL and C1
// controls go too, tabs become spaces, and runs of whitespace collapse.
func Clean(s string) string {
	if s == "" {
		return ""
	}
	var b strings.Builder
	b.Grow(len(s))
	r := []rune(s)
	for i := 0; i < len(r); {
		c := r[i]
		switch {
		case c == 0x1b && i+1 < len(r):
			i = skipEscape(r, i+1, r[i+1])
		case c == 0x1b:
			i++
		case c == 0x9b, c == 0x9d, c == 0x90, c == 0x98, c == 0x9e, c == 0x9f:
			// The 8-bit forms of CSI, OSC and the string introducers.
			i = skipEscape(r, i, c-0x40)
		case c == '\t':
			b.WriteByte(' ')
			i++
		case c < 0x20, c == 0x7f, c >= 0x80 && c <= 0x9f:
			i++
		default:
			b.WriteRune(c)
			i++
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// skipEscape returns the index just past the escape sequence whose introducer
// is kind ('[' for CSI, ']' for OSC, 'P'/'X'/'^'/'_' for the string controls)
// and whose body starts at i.
func skipEscape(r []rune, i int, kind rune) int {
	switch kind {
	case '[':
		i++ // the '[' itself, or nothing for the 8-bit form handled below
		for i < len(r) && !(r[i] >= 0x40 && r[i] <= 0x7e) {
			i++
		}
		if i < len(r) {
			i++ // the final byte
		}
		return i
	case ']', 'P', 'X', '^', '_':
		i++
		for i < len(r) {
			if r[i] == 0x07 || r[i] == 0x9c {
				return i + 1
			}
			if r[i] == 0x1b && i+1 < len(r) && r[i+1] == '\\' {
				return i + 2
			}
			i++
		}
		return i
	default:
		return i + 1
	}
}

// hasControl reports whether s carries anything a terminal would act on. A
// URI is dropped outright rather than cleaned: a link with an escape sequence
// spliced into it is not a link the user should be offered.
func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return true
		}
	}
	return false
}
