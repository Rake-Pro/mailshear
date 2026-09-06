package keep

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		subject string
		want    bool
		cat     string
	}{
		// receipts and money
		{"Your receipt from Acme", true, CategoryReceipt},
		{"Receipt #4471", true, CategoryReceipt},
		{"Invoice 2026-09 is ready", true, CategoryReceipt},
		{"Your September bill is ready", true, CategoryReceipt},
		{"Billing update for your account", true, CategoryReceipt},
		{"Your monthly statement", true, CategoryReceipt},
		{"Payment received - thank you", true, CategoryReceipt},
		{"Payment confirmation for order 12", true, CategoryReceipt},
		{"Invoice paid", true, CategoryReceipt},
		{"Your purchase is complete", true, CategoryReceipt},
		{"Transaction alert", true, CategoryReceipt},
		{"Your refund is on the way", true, CategoryReceipt},

		// orders and shipping
		{"Order confirmation", true, CategoryOrder},
		{"Your order has been placed", true, CategoryOrder},
		{"Order #99213 update", true, CategoryOrder},
		{"Your package has shipped", true, CategoryOrder},
		{"Shipping confirmation", true, CategoryOrder},
		{"Out for delivery today", true, CategoryOrder},
		{"Delivered: 1 package", true, CategoryOrder},
		{"Tracking number for your parcel", true, CategoryOrder},
		{"Return started", true, CategoryOrder},

		// travel
		{"Your itinerary for Tuesday", true, CategoryBooking},
		{"Booking confirmation - Hotel Zed", true, CategoryBooking},
		{"Reservation details", true, CategoryBooking},
		{"Your e-ticket is attached", true, CategoryBooking},
		{"Boarding pass ready", true, CategoryBooking},
		{"Check-in opens in 24 hours", true, CategoryBooking},

		// security
		{"Your verification code is 123456", true, CategorySecurity},
		{"One-time code", true, CategorySecurity},
		{"Security alert on your account", true, CategorySecurity},
		{"New sign-in from Chrome", true, CategorySecurity},
		{"Sign-in attempt blocked", true, CategorySecurity},
		{"Password reset requested", true, CategorySecurity},
		{"Reset your password", true, CategorySecurity},
		{"Two-factor authentication enabled", true, CategorySecurity},
		{"2FA is now on", true, CategorySecurity},
		{"Confirm your email address", true, CategorySecurity},
		{"Verify your account to continue", true, CategorySecurity},

		// account documents
		{"Your account statement is ready", true, CategoryReceipt}, // "statement" hits receipt first
		{"Your tax document is available", true, CategoryAccount},
		{"Your 1099 is ready", true, CategoryAccount},
		{"W-2 available for download", true, CategoryAccount},
		{"Policy document update", true, CategoryAccount},

		// word boundaries: these must NOT match
		{"Newsletter receipts up 20%", false, ""},
		{"A billion reasons to switch", false, ""},
		{"Billionaire mindset weekly", false, ""},
		{"The best statements of 2026", false, ""},
		{"Unpaid leave explained", false, ""},
		{"Reordering your bookshelf", false, ""},
		{"Returning to the office", false, ""},
		{"12099 steps to fitness", false, ""},

		// plain marketing
		{"Half price today only", false, ""},
		{"Weekly digest", false, ""},
		{"", false, ""},
		{"   ", false, ""},
	}

	for _, tc := range cases {
		got, cat := Match(tc.subject)
		if got != tc.want {
			t.Errorf("Match(%q) = %v (%s), want %v", tc.subject, got, cat, tc.want)
			continue
		}
		if got && cat != tc.cat {
			t.Errorf("Match(%q) category = %q, want %q", tc.subject, cat, tc.cat)
		}
	}
}

func TestMatcherDisabled(t *testing.T) {
	m, err := New(false, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if m.Enabled() {
		t.Fatalf("matcher built with enabled=false reports enabled")
	}
	if ok, _ := m.Match("Your receipt from Acme"); ok {
		t.Fatalf("disabled matcher still matched")
	}
}

func TestZeroMatcherKeepsReceipts(t *testing.T) {
	var m Matcher
	if !m.Enabled() {
		t.Fatalf("the zero Matcher must be enabled")
	}
	ok, cat := m.Match("Your receipt from Acme")
	if !ok || cat != CategoryReceipt {
		t.Fatalf("zero matcher = %v %q", ok, cat)
	}
}

func TestMatcherExtraPatterns(t *testing.T) {
	m, err := New(true, []string{"Rechnung", "/renewal (notice|reminder)/", "  "})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	cases := []struct {
		subject string
		want    bool
		cat     string
	}{
		{"Ihre rechnung fuer September", true, CategoryCustom},
		{"Renewal notice for your plan", true, CategoryCustom},
		{"renewal reminder", true, CategoryCustom},
		{"Your receipt from Acme", true, CategoryReceipt},
		{"Half price today only", false, ""},
	}
	for _, tc := range cases {
		got, cat := m.Match(tc.subject)
		if got != tc.want || (got && cat != tc.cat) {
			t.Errorf("Match(%q) = %v %q, want %v %q", tc.subject, got, cat, tc.want, tc.cat)
		}
	}
}

func TestMatcherRejectsBadRegexp(t *testing.T) {
	if _, err := New(true, []string{"/[/"}); err == nil {
		t.Fatalf("expected an error for an unparseable /regexp/")
	}
}
