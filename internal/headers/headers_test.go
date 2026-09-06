package headers

import (
	"reflect"
	"testing"
)

func TestParseListUnsubscribe(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{
			name: "mailchimp two brackets comma separated",
			raw:  "<mailto:unsub-abc123@list.mailchimp.example?subject=unsubscribe>, <https://list.mailchimp.example/unsubscribe?u=abc&id=def>",
			want: []string{
				"mailto:unsub-abc123@list.mailchimp.example?subject=unsubscribe",
				"https://list.mailchimp.example/unsubscribe?u=abc&id=def",
			},
		},
		{
			name: "sendgrid folded across two lines",
			raw:  "<https://u1234.ct.sendgrid.example/unsubscribe/abcXYZ>,\r\n <mailto:unsubscribe@sendgrid.example>",
			want: []string{
				"https://u1234.ct.sendgrid.example/unsubscribe/abcXYZ",
				"mailto:unsubscribe@sendgrid.example",
			},
		},
		{
			name: "substack folded with tab continuation",
			raw:  "<https://substack.example/action/disable_email?token=xyz>\n\t, <mailto:a+unsubscribe@substack.example>",
			want: []string{
				"https://substack.example/action/disable_email?token=xyz",
				"mailto:a+unsubscribe@substack.example",
			},
		},
		{
			name: "amazon ses single https",
			raw:  "<https://email.amazonses.example/unsub/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA>",
			want: []string{"https://email.amazonses.example/unsub/AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		},
		{
			name: "google groups mailto with subject and unsubscribe body",
			raw:  "<mailto:group+unsubscribe@googlegroups.example>",
			want: []string{"mailto:group+unsubscribe@googlegroups.example"},
		},
		{
			name: "linkedin mailto with subject and body query params",
			raw:  "<mailto:unsubscribe@linkedin.example?subject=Unsubscribe&body=unsubscribe-token-abc123>",
			want: []string{"mailto:unsubscribe@linkedin.example?subject=Unsubscribe&body=unsubscribe-token-abc123"},
		},
		{
			name: "mailto query contains comma inside brackets not split",
			raw:  "<mailto:list@example.com?subject=unsubscribe&body=please,remove,me>",
			want: []string{"mailto:list@example.com?subject=unsubscribe&body=please,remove,me"},
		},
		{
			name: "bare urls without angle brackets",
			raw:  "https://a.example.com/unsub, https://b.example.com/unsub",
			want: []string{"https://a.example.com/unsub", "https://b.example.com/unsub"},
		},
		{
			name: "bare url no brackets no comma just space",
			raw:  "https://example.com/unsub",
			want: []string{"https://example.com/unsub"},
		},
		{
			name: "junk text around a bracketed uri",
			raw:  "please click here to unsubscribe: <https://example.com/unsub> or reply STOP",
			want: []string{"https://example.com/unsub"},
		},
		{
			name: "junk only text no uri",
			raw:  "To unsubscribe, please contact your administrator",
			want: nil,
		},
		{
			name: "empty value",
			raw:  "",
			want: nil,
		},
		{
			name: "whitespace only value",
			raw:  "   \t  ",
			want: nil,
		},
		{
			name: "crlf folding between two bracketed uris",
			raw:  "<https://example.com/one>\r\n <https://example.com/two>",
			want: []string{"https://example.com/one", "https://example.com/two"},
		},
		{
			name: "duplicates removed preserving order",
			raw:  "<https://example.com/unsub>, <https://example.com/unsub>, <mailto:x@example.com>",
			want: []string{"https://example.com/unsub", "mailto:x@example.com"},
		},
		{
			name: "hand rolled sender mixed case scheme",
			raw:  "<HTTPS://example.com/unsub>",
			want: []string{"HTTPS://example.com/unsub"},
		},
		{
			name: "non unsub scheme rejected",
			raw:  "<ftp://example.com/unsub>, <https://example.com/ok>",
			want: []string{"https://example.com/ok"},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseListUnsubscribe(c.raw)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseListUnsubscribe(%q) = %#v, want %#v", c.raw, got, c.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name          string
		listUnsub     string
		listUnsubPost string
		want          Unsubscribe
	}{
		{
			name:          "oneclick with https and post header",
			listUnsub:     "<https://example.com/unsub?t=abc>",
			listUnsubPost: "List-Unsubscribe=One-Click",
			want: Unsubscribe{
				URIs:     []string{"https://example.com/unsub?t=abc"},
				HTTPS:    []string{"https://example.com/unsub?t=abc"},
				OneClick: true,
				Method:   MethodOneClick,
			},
		},
		{
			name:          "post header case insensitive",
			listUnsub:     "<https://example.com/unsub>",
			listUnsubPost: "list-unsubscribe=one-click",
			want: Unsubscribe{
				URIs:     []string{"https://example.com/unsub"},
				HTTPS:    []string{"https://example.com/unsub"},
				OneClick: true,
				Method:   MethodOneClick,
			},
		},
		{
			name:          "post header present but no http uri stays mailto not oneclick",
			listUnsub:     "<mailto:unsub@example.com>",
			listUnsubPost: "List-Unsubscribe=One-Click",
			want: Unsubscribe{
				URIs:     []string{"mailto:unsub@example.com"},
				Mailto:   []string{"mailto:unsub@example.com"},
				OneClick: false,
				Method:   MethodMailto,
			},
		},
		{
			name:          "http and mailto no post header is http method",
			listUnsub:     "<https://example.com/unsub>, <mailto:unsub@example.com>",
			listUnsubPost: "",
			want: Unsubscribe{
				URIs:     []string{"https://example.com/unsub", "mailto:unsub@example.com"},
				HTTPS:    []string{"https://example.com/unsub"},
				Mailto:   []string{"mailto:unsub@example.com"},
				OneClick: false,
				Method:   MethodHTTP,
			},
		},
		{
			name:          "https sorted before http",
			listUnsub:     "<http://example.com/plain>, <https://example.com/secure>",
			listUnsubPost: "",
			want: Unsubscribe{
				URIs:   []string{"http://example.com/plain", "https://example.com/secure"},
				HTTPS:  []string{"https://example.com/secure", "http://example.com/plain"},
				Method: MethodHTTP,
			},
		},
		{
			name:          "mailto only no post header",
			listUnsub:     "<mailto:unsub@example.com>",
			listUnsubPost: "",
			want: Unsubscribe{
				URIs:   []string{"mailto:unsub@example.com"},
				Mailto: []string{"mailto:unsub@example.com"},
				Method: MethodMailto,
			},
		},
		{
			name:          "empty list unsub is none regardless of post header",
			listUnsub:     "",
			listUnsubPost: "List-Unsubscribe=One-Click",
			want:          Unsubscribe{Method: MethodNone},
		},
		{
			name:          "junk only value is none",
			listUnsub:     "no links here at all",
			listUnsubPost: "",
			want:          Unsubscribe{Method: MethodNone},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify(c.listUnsub, c.listUnsubPost)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("Classify(%q, %q) = %#v, want %#v", c.listUnsub, c.listUnsubPost, got, c.want)
			}
		})
	}
}

func TestParseListID(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"with description and bracket", "Weekly Digest <digest.list-id.example.com>", "digest.list-id.example.com"},
		{"google groups style", "My Group <mygroup.googlegroups.example.com>", "mygroup.googlegroups.example.com"},
		{"bracket already lowercase", "<foo.example.com>", "foo.example.com"},
		{"bracket mixed case lowercased", "<Foo.Example.COM>", "foo.example.com"},
		{"no bracket falls back to whole value", "foo.example.com", "foo.example.com"},
		{"empty brackets", "<>", ""},
		{"empty brackets with description", "Weekly Digest <>", ""},
		{"empty value", "", ""},
		{"whitespace only", "   ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseListID(c.raw)
			if got != c.want {
				t.Errorf("ParseListID(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestParseFrom(t *testing.T) {
	cases := []struct {
		name        string
		raw         string
		wantDisplay string
		wantAddr    string
	}{
		{
			name:        "rfc2047 utf-8 base64 display name",
			raw:         "=?UTF-8?B?TWFpbGNoaW1w?= <newsletter@mailchimp.example>",
			wantDisplay: "Mailchimp",
			wantAddr:    "newsletter@mailchimp.example",
		},
		{
			name:        "rfc2047 iso-8859-1 q-encoded display name",
			raw:         "=?ISO-8859-1?Q?Caf=E9_News?= <news@example.com>",
			wantDisplay: "Café News",
			wantAddr:    "news@example.com",
		},
		{
			name:        "plain display name",
			raw:         "Google Groups <no-reply@googlegroups.example>",
			wantDisplay: "Google Groups",
			wantAddr:    "no-reply@googlegroups.example",
		},
		{
			name:        "bare address no display name",
			raw:         "orders@amazon.example",
			wantDisplay: "",
			wantAddr:    "orders@amazon.example",
		},
		{
			name:        "quoted display name with comma",
			raw:         "\"LinkedIn, Notifications\" <notifications@linkedin.example>",
			wantDisplay: "LinkedIn, Notifications",
			wantAddr:    "notifications@linkedin.example",
		},
		{
			name:        "display name with parenthetical comment",
			raw:         "Support Team (do not reply) <support@example.com>",
			wantDisplay: "Support Team",
			wantAddr:    "support@example.com",
		},
		{
			name:        "address uppercased is lowercased",
			raw:         "Sender Name <Sender@Example.COM>",
			wantDisplay: "Sender Name",
			wantAddr:    "sender@example.com",
		},
		{
			name:        "empty value",
			raw:         "",
			wantDisplay: "",
			wantAddr:    "",
		},
		{
			name:        "total junk no address anywhere",
			raw:         "not an email header at all",
			wantDisplay: "",
			wantAddr:    "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gotDisplay, gotAddr := ParseFrom(c.raw)
			if gotDisplay != c.wantDisplay || gotAddr != c.wantAddr {
				t.Errorf("ParseFrom(%q) = (%q, %q), want (%q, %q)", c.raw, gotDisplay, gotAddr, c.wantDisplay, c.wantAddr)
			}
		})
	}
}

func TestDecodeSubject(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "rfc2047 encoded subject",
			raw:  "=?UTF-8?B?WW91ciB3ZWVrbHkgZGlnZXN0?=",
			want: "Your weekly digest",
		},
		{
			name: "plain subject",
			raw:  "50% off everything this weekend",
			want: "50% off everything this weekend",
		},
		{
			name: "folded subject collapses whitespace",
			raw:  "This is a very\r\n long subject line",
			want: "This is a very long subject line",
		},
		{
			name: "extra internal whitespace collapsed",
			raw:  "Too   many    spaces",
			want: "Too many spaces",
		},
		{
			name: "empty subject",
			raw:  "",
			want: "",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecodeSubject(c.raw)
			if got != c.want {
				t.Errorf("DecodeSubject(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

func TestSenderKey(t *testing.T) {
	cases := []struct {
		name   string
		listID string
		from   string
		want   string
	}{
		{"list id present", "digest.example.com", "sender@example.com", "list:digest.example.com"},
		{"no list id falls back to address", "", "Sender@Example.com", "addr:sender@example.com"},
		{"no list id and no address", "", "", "addr:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SenderKey(c.listID, c.from)
			if got != c.want {
				t.Errorf("SenderKey(%q, %q) = %q, want %q", c.listID, c.from, got, c.want)
			}
		})
	}
}

func TestDomainKey(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"simple domain", "news@mailchimp.example", "mailchimp.example"},
		{"subdomain collapses to registrable domain", "news@list.mailchimp.example.com", "example.com"},
		{"co.uk registrable domain", "alerts@subscriptions.bbc.co.uk", "bbc.co.uk"},
		{"uppercase address lowercased", "ALERTS@EXAMPLE.COM", "example.com"},
		{"ip-like domain falls back to raw", "user@192.168.1.1", "192.168.1.1"},
		{"address literal drops brackets", "user@[192.168.1.1]", "192.168.1.1"},
		{"trailing dot stripped", "news@mailchimp.example.", "mailchimp.example"},
		{"trailing dot stripped from subdomain", "news@list.mailchimp.example.com.", "example.com"},
		{"domain that is only a dot returns empty", "user@.", ""},
		{"single label invalid domain falls back to raw", "user@localhost", "localhost"},
		{"no at sign returns empty", "not-an-address", ""},
		{"empty address returns empty", "", ""},
		{"trailing at sign returns empty", "user@", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DomainKey(c.addr)
			if got != c.want {
				t.Errorf("DomainKey(%q) = %q, want %q", c.addr, got, c.want)
			}
		})
	}
}

func TestMailtoParts(t *testing.T) {
	cases := []struct {
		name        string
		uri         string
		wantAddr    string
		wantSubject string
		wantBody    string
		wantOK      bool
	}{
		{
			name:        "subject and body percent encoded",
			uri:         "mailto:unsubscribe@linkedin.example?subject=Unsubscribe%20Request&body=Please%20remove%20me%20from%20this%20list",
			wantAddr:    "unsubscribe@linkedin.example",
			wantSubject: "Unsubscribe Request",
			wantBody:    "Please remove me from this list",
			wantOK:      true,
		},
		{
			name:     "no query params",
			uri:      "mailto:unsub@example.com",
			wantAddr: "unsub@example.com",
			wantOK:   true,
		},
		{
			name:     "address uppercased is lowercased",
			uri:      "mailto:Unsub@Example.COM",
			wantAddr: "unsub@example.com",
			wantOK:   true,
		},
		{
			name:   "not a mailto uri",
			uri:    "https://example.com/unsub",
			wantOK: false,
		},
		{
			name:   "invalid uri",
			uri:    "://not a uri",
			wantOK: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, subject, body, ok := MailtoParts(c.uri)
			if ok != c.wantOK || addr != c.wantAddr || subject != c.wantSubject || body != c.wantBody {
				t.Errorf("MailtoParts(%q) = (%q, %q, %q, %v), want (%q, %q, %q, %v)",
					c.uri, addr, subject, body, ok, c.wantAddr, c.wantSubject, c.wantBody, c.wantOK)
			}
		})
	}
}

func TestParseListUnsubscribeFoldedInsideURI(t *testing.T) {
	raw := "<https://example.com/unsub/\r\n very-long-token>, <mailto:leave@example.com>"
	want := []string{"https://example.com/unsub/very-long-token", "mailto:leave@example.com"}
	got := ParseListUnsubscribe(raw)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseListUnsubscribe(%q) = %#v, want %#v", raw, got, want)
	}
}

func TestParseListUnsubscribeRejectsSchemeOnly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want []string
	}{
		{"https with no host", "<https://>", nil},
		{"http with no host", "<http://>", nil},
		{"mailto with no target", "<mailto:>", nil},
		{"bare mailto with no target", "mailto:", nil},
		{"scheme only alongside a usable uri", "<https://>, <https://example.com/u>", []string{"https://example.com/u"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseListUnsubscribe(c.raw)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("ParseListUnsubscribe(%q) = %#v, want %#v", c.raw, got, c.want)
			}
		})
	}
}

func TestMailtoPartsStripsControlCharacters(t *testing.T) {
	cases := []struct {
		name              string
		uri               string
		addr, subj, body_ string
	}{
		{
			name: "crlf in subject is removed",
			uri:  "mailto:list@example.com?subject=unsub%0D%0ABcc:%20victim@example.org&body=please",
			addr: "list@example.com", subj: "unsubBcc: victim@example.org", body_: "please",
		},
		{
			name: "other control characters are removed from the subject",
			uri:  "mailto:list@example.com?subject=a%09b%00c%1Bd",
			addr: "list@example.com", subj: "abcd", body_: "",
		},
		{
			name: "body keeps newlines but normalized to lf",
			uri:  "mailto:list@example.com?body=one%0D%0Atwo%0Dthree%0Afour",
			addr: "list@example.com", subj: "", body_: "one\ntwo\nthree\nfour",
		},
		{
			name: "body drops other control characters",
			uri:  "mailto:list@example.com?body=x%00y%1Bz",
			addr: "list@example.com", subj: "", body_: "xyz",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, subject, body, ok := MailtoParts(c.uri)
			if !ok {
				t.Fatalf("MailtoParts(%q) not ok", c.uri)
			}
			if addr != c.addr || subject != c.subj || body != c.body_ {
				t.Fatalf("MailtoParts(%q) = (%q, %q, %q), want (%q, %q, %q)",
					c.uri, addr, subject, body, c.addr, c.subj, c.body_)
			}
			for _, r := range subject {
				if r < 0x20 || r == 0x7f {
					t.Fatalf("subject %q still holds control character %U", subject, r)
				}
			}
			for _, r := range body {
				if r == '\n' || r == '\t' {
					continue
				}
				if r < 0x20 || r == 0x7f {
					t.Fatalf("body %q still holds control character %U", body, r)
				}
			}
		})
	}
}

func TestParseFromLenientEncodedWords(t *testing.T) {
	cases := map[string]string{
		`"=?utf-8?B?Q29zdGNvIEFueXdoZXJlIFZpc2E=?= =?utf-8?B?Q2FyZA==?=" <x@y.com>`: "Costco Anywhere VisaCard",
		`=?utf-8?B?Q29zdGNvIEFueXdoZXJlIFZpc2E?= =?utf-8?B?Q2FyZA?= <x@y.com>`:      "Costco Anywhere VisaCard",
		`=?utf-8?B?Q29zdGNvIEFueXdoZXJlIFZpc2E=?==?utf-8?B?Q2FyZA==?= <x@y.com>`:    "Costco Anywhere VisaCard",
		`"=?UTF-8?Q?Caf=C3=A9_News?=" <x@y.com>`:                                    "Caf\u00e9 News",
		`"=?utf-8?B?not*base64?=" <x@y.com>`:                                        "=?utf-8?B?not*base64?=",
	}
	for raw, want := range cases {
		got, addr := ParseFrom(raw)
		if got != want || addr != "x@y.com" {
			t.Errorf("%q: got %q %q, want %q", raw, got, addr, want)
		}
	}
	if got := DecodeSubject(`"=?utf-8?B?SGVsbG8=?="`); got != `"Hello"` {
		t.Errorf("subject: %q", got)
	}
}
