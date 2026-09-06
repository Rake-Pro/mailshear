package plan

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func TestNewRunIDShape(t *testing.T) {
	re := regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		id := NewRunID()
		if !re.MatchString(id) {
			t.Fatalf("run id %q does not match the expected shape", id)
		}
		seen[id] = true
	}
	if len(seen) < 2 {
		t.Fatalf("expected distinct run ids, got %d unique of 20", len(seen))
	}
}

func TestPath(t *testing.T) {
	got := Path("/data/mailshear", "20260906-153012-ab12")
	want := filepath.Join("/data/mailshear", "plans", "20260906-153012-ab12.yaml")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
}

func TestWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "20260906-153012-ab12")

	p := &Plan{
		RunID:     "20260906-153012-ab12",
		Account:   "personal",
		CreatedAt: time.Date(2026, 9, 6, 15, 30, 12, 0, time.UTC),
		Snapshot: Snapshot{
			MessageCount: 4211,
			LatestSeen:   time.Date(2026, 9, 5, 8, 0, 0, 0, time.UTC),
		},
		Decisions: []Entry{
			{
				SenderKey: "list:news.example.com", Display: "Example News",
				Address: "news@example.com", Unsubscribe: true, DeleteMatched: true,
				Method: "oneclick", URIs: []string{"https://example.com/u"}, OneClick: true,
			},
			{
				SenderKey: "addr:deals@example.net", Display: "Deals",
				DeleteAll: true, Method: "none",
			},
		},
	}

	if err := Write(path, p); err != nil {
		t.Fatalf("Write: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat plan: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("plan mode = %o, want 0600", got)
	}
	dinfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat plans dir: %v", err)
	}
	if got := dinfo.Mode().Perm(); got != 0700 {
		t.Fatalf("plans dir mode = %o, want 0700", got)
	}

	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, p)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read plans dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the plan file left behind, got %d entries", len(entries))
	}
}

func TestWriteIsAtomicOverExisting(t *testing.T) {
	dir := t.TempDir()
	path := Path(dir, "run")
	if err := Write(path, &Plan{RunID: "run", Account: "a"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := Write(path, &Plan{RunID: "run", Account: "b"}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Account != "b" {
		t.Fatalf("expected the second write to win, got account %q", got.Account)
	}
}

func TestReadRejectsUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	body := "run_id: r\naccount: a\nnot_a_field: 1\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Read(path); err == nil {
		t.Fatalf("expected an error for an unknown field")
	}
}

func TestSummary(t *testing.T) {
	p := &Plan{Decisions: []Entry{
		{Unsubscribe: true},
		{Unsubscribe: true, DeleteMatched: true},
		{DeleteAll: true},
		{DeleteMatched: true, DeleteAll: true},
	}}
	unsub, del, delAll := p.Summary()
	if unsub != 2 || del != 2 || delAll != 2 {
		t.Fatalf("Summary = %d, %d, %d; want 2, 2, 2", unsub, del, delAll)
	}
}
