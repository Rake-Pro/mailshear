// Package keep recognises transactional mail from its subject line: receipts,
// bills, invoices, statements, order and payment confirmations, travel
// bookings and security notices.
//
// This is the "never delete this" side of the safety model in docs/design.md
// section 5. It is a heuristic over a phrase list and it is deliberately
// biased toward keeping: a false positive costs one undeleted newsletter, a
// false negative costs a receipt. Nothing here reads message bodies.
package keep

import (
	"fmt"
	"regexp"
	"strings"
)

// Categories, in the order Match tests them. The first hit wins.
const (
	CategoryReceipt  = "receipt"
	CategoryOrder    = "order"
	CategoryBooking  = "booking"
	CategorySecurity = "security"
	CategoryAccount  = "account"
	// CategoryCustom is returned for a phrase from the user's keep_subjects.
	CategoryCustom = "custom"
)

// Phrases is the built-in list, by category. Matching is case-insensitive and
// respects word boundaries, so "bill" does not match "billion" and "receipt"
// does not match "receipts up 20%".
var Phrases = []struct {
	Category string
	Phrases  []string
}{
	{CategoryReceipt, []string{
		"receipt", "your receipt", "invoice", "bill", "billing", "statement",
		"payment received", "payment confirmation", "paid", "purchase",
		"transaction", "refund",
	}},
	{CategoryOrder, []string{
		"order confirmation", "your order", "order #", "has shipped",
		"shipping confirmation", "out for delivery", "delivered",
		"tracking number", "return",
	}},
	{CategoryBooking, []string{
		"itinerary", "booking confirmation", "reservation", "e-ticket",
		"boarding pass", "check-in",
	}},
	{CategorySecurity, []string{
		"verification code", "one-time code", "security alert", "new sign-in",
		"sign-in attempt", "password reset", "reset your password",
		"two-factor", "2fa", "confirm your email", "verify your account",
	}},
	{CategoryAccount, []string{
		"your account statement", "tax document", "1099", "w-2",
		"policy document",
	}},
}

// builtin holds one compiled alternation per category, in Phrases order.
var builtin = compileBuiltin()

type categoryRe struct {
	category string
	re       *regexp.Regexp
}

func compileBuiltin() []categoryRe {
	out := make([]categoryRe, 0, len(Phrases))
	for _, group := range Phrases {
		alts := make([]string, 0, len(group.Phrases))
		for _, p := range group.Phrases {
			alts = append(alts, phrasePattern(p))
		}
		out = append(out, categoryRe{
			category: group.Category,
			re:       regexp.MustCompile(`(?i)(?:` + strings.Join(alts, "|") + `)`),
		})
	}
	return out
}

// phrasePattern turns one phrase into a regexp source. Runs of whitespace in
// the subject are treated as one space, and a word boundary is anchored only
// on the ends that are word characters: "order #" must still match "Order
// #1234", where a trailing \b would never fire.
func phrasePattern(phrase string) string {
	words := strings.Fields(phrase)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	body := strings.Join(words, `\s+`)
	r := []rune(phrase)
	if len(r) > 0 && isWordRune(r[0]) {
		body = `\b` + body
	}
	if len(r) > 0 && isWordRune(r[len(r)-1]) {
		body += `\b`
	}
	return body
}

func isWordRune(r rune) bool {
	return r == '_' ||
		(r >= '0' && r <= '9') ||
		(r >= 'a' && r <= 'z') ||
		(r >= 'A' && r <= 'Z')
}

// Match reports whether subject looks transactional, and which category hit.
// It uses the built-in phrase list only; user patterns go through Matcher.
func Match(subject string) (bool, string) {
	if strings.TrimSpace(subject) == "" {
		return false, ""
	}
	for _, c := range builtin {
		if c.re.MatchString(subject) {
			return true, c.category
		}
	}
	return false, ""
}

// Matcher is the built-in list plus the user's extra keep_subjects patterns.
// The zero Matcher is enabled with no extras, so a caller that forgot to
// configure one still keeps receipts.
type Matcher struct {
	off   bool
	extra []*regexp.Regexp
}

// New builds a Matcher. enabled is protect.keep_transactional; patterns are
// protect.keep_subjects, each either a case-insensitive substring or a
// /regexp/ between slashes.
func New(enabled bool, patterns []string) (Matcher, error) {
	m := Matcher{off: !enabled}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		re, err := compileUser(p)
		if err != nil {
			return Matcher{}, err
		}
		m.extra = append(m.extra, re)
	}
	return m, nil
}

func compileUser(pattern string) (*regexp.Regexp, error) {
	if len(pattern) >= 2 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		body := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(`(?i)` + body)
		if err != nil {
			return nil, fmt.Errorf("keep: keep_subjects %q: %w", pattern, err)
		}
		return re, nil
	}
	return regexp.MustCompile(`(?i)` + regexp.QuoteMeta(pattern)), nil
}

// Enabled reports whether the keep rule is in force.
func (m Matcher) Enabled() bool { return !m.off }

// Match applies the built-in list and then the user's patterns.
func (m Matcher) Match(subject string) (bool, string) {
	if m.off {
		return false, ""
	}
	if ok, cat := Match(subject); ok {
		return true, cat
	}
	for _, re := range m.extra {
		if re.MatchString(subject) {
			return true, CategoryCustom
		}
	}
	return false, ""
}
