package headers

import (
	"encoding/base64"
	"strings"
	"testing"
)

// A mail header is written by whoever sent the message, so an escape sequence
// in one would be executed by the terminal that prints it: OSC 52 writes the
// clipboard, OSC 0 renames the window, CSI 2J clears the screen.
func TestCleanRemovesTerminalEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"csi and osc", "\x1b[2J\x1b]0;pwned\x07Evil\x1b[31m Sender", "Evil Sender"},
		{"osc 52 clipboard", "sub\x1b]52;c;cGFzcw==\x07ject", "subject"},
		{"osc ended by st", "a\x1b]0;x\x1b\\b", "ab"},
		{"bare controls", "a\x00b\x7fc", "abc"},
		{"eight bit csi", "a\u009b31mb", "ab"},
		{"other c1", "a\u0085b", "ab"},
		{"tab becomes a space", "a\tb", "a b"},
		{"whitespace collapses", "  a   b  ", "a b"},
		{"plain text is untouched", "Acme Newsletter", "Acme Newsletter"},
		{"non-ascii text survives", "Grüße", "Grüße"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Clean(tc.in); got != tc.want {
				t.Fatalf("Clean(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParseFromCleansTerminalEscapes(t *testing.T) {
	display, address := ParseFrom("\"\x1b[2J\x1b]0;pwned\x07Evil\x1b[31m Sender\" <news@example.com>")
	if display != "Evil Sender" {
		t.Fatalf("display = %q, want %q", display, "Evil Sender")
	}
	if address != "news@example.com" {
		t.Fatalf("address = %q", address)
	}

	if _, addr := ParseFrom("Evil \x1b]0;x\x07 <news@example.com>"); strings.ContainsRune(addr, 0x1b) {
		t.Fatalf("address = %q, want no escape", addr)
	}
}

func TestDecodeSubjectCleansTerminalEscapes(t *testing.T) {
	// The clipboard-writing sequence arrives base64 inside an encoded word,
	// so it only appears after RFC 2047 decoding.
	raw := "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte("sub\x1b]52;c;cGFzcw==\x07ject")) + "?="
	if got := DecodeSubject(raw); got != "subject" {
		t.Fatalf("DecodeSubject = %q, want %q", got, "subject")
	}
	if got := DecodeSubject("plain \x1b[31mred\x1b[0m"); got != "plain red" {
		t.Fatalf("DecodeSubject = %q", got)
	}
}

func TestParseListIDCleansTerminalEscapes(t *testing.T) {
	if got := ParseListID("Some List <news.\x1b[2Jexample.com>"); got != "news.example.com" {
		t.Fatalf("ParseListID = %q", got)
	}
}

// A link with an escape sequence spliced into it is dropped rather than
// cleaned: what is left is not the address the sender wrote.
func TestParseListUnsubscribeDropsURIsWithControls(t *testing.T) {
	got := ParseListUnsubscribe("<https://good.example/u>, <https://evil.example/\x1b[2J>")
	if len(got) != 1 || got[0] != "https://good.example/u" {
		t.Fatalf("uris = %q, want only the clean one", got)
	}
	if len(ParseListUnsubscribe("<https://evil.example/\x07x>")) != 0 {
		t.Fatal("a URI with a BEL in it was kept")
	}
}
