package text

import "testing"

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{0: "0 B", 512: "512 B", 2048: "2.0 KB", 5 * 1024 * 1024: "5.0 MB"}
	for in, want := range cases {
		if got := HumanBytes(in); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestJoinCounts(t *testing.T) {
	got := JoinCounts(map[string]int{"oneclick": 3, "http": 1, "mailto": 2})
	if want := "http 1, mailto 2, oneclick 3"; got != want {
		t.Errorf("JoinCounts = %q, want %q", got, want)
	}
	if got := JoinCounts(nil); got != "" {
		t.Errorf("JoinCounts(nil) = %q, want empty", got)
	}
}
