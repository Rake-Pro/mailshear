// Package plan implements the plan file: the reviewed set of sender decisions
// that apply later consumes.
package plan

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/Rake-Pro/mailshear/internal/store"
)

// Plan is the on-disk review outcome for one account.
type Plan struct {
	RunID     string    `yaml:"run_id"`
	Account   string    `yaml:"account"`
	CreatedAt time.Time `yaml:"created_at"`
	Snapshot  Snapshot  `yaml:"snapshot"`
	Decisions []Entry   `yaml:"decisions"`
}

// Snapshot records the state of the local database the plan was built against,
// so apply can warn when the database has moved on since.
type Snapshot struct {
	MessageCount int       `yaml:"message_count"`
	LatestSeen   time.Time `yaml:"latest_seen"`
}

// Entry is one sender's decision.
type Entry struct {
	SenderKey     string `yaml:"sender_key"`
	Display       string `yaml:"display"`
	Address       string `yaml:"address,omitempty"`
	Unsubscribe   bool   `yaml:"unsubscribe"`
	DeleteMatched bool   `yaml:"delete_matched"`
	DeleteAll     bool   `yaml:"delete_all"`
	// IncludeKept waives the transactional-mail rule for this sender:
	// receipts, bills and security mail are deleted with everything else.
	IncludeKept bool     `yaml:"include_kept,omitempty"`
	Method      string   `yaml:"method"`
	URIs        []string `yaml:"uris,omitempty"`
	OneClick    bool     `yaml:"one_click,omitempty"`
}

// NewRunID returns a run id of the form 20260906-153012-ab12: a UTC timestamp
// plus two random bytes, so two runs in the same second do not collide.
func NewRunID() string {
	var b [2]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any supported platform; fall back to
		// the timestamp alone rather than panicking mid-review.
		return time.Now().UTC().Format("20060102-150405")
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// Path is where a plan for runID lives under dataDir.
func Path(dataDir, runID string) string {
	return filepath.Join(dataDir, "plans", runID+".yaml")
}

// Write serializes p to path, creating the directory with mode 0700 and the
// file with mode 0600. The write is atomic: a temp file in the same directory
// is renamed over the target.
func Write(path string, p *Plan) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("plan: mkdir %s: %w", dir, err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(p); err != nil {
		return fmt.Errorf("plan: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("plan: encode: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".plan-*.yaml")
	if err != nil {
		return fmt.Errorf("plan: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("plan: chmod %s: %w", tmpName, err)
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("plan: write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("plan: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("plan: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("plan: rename into %s: %w", path, err)
	}
	return nil
}

// Read loads a plan file, rejecting unknown keys so a typo in a hand-edited
// plan is an error rather than a silently ignored field.
func Read(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("plan: reading %s: %w", path, err)
	}
	var p Plan
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("plan: parsing %s: %w", path, err)
	}
	return &p, nil
}

// Summary counts the entries by action.
func (p *Plan) Summary() (unsub, del, delAll int) {
	for _, e := range p.Decisions {
		if e.Unsubscribe {
			unsub++
		}
		if e.DeleteMatched {
			del++
		}
		if e.DeleteAll {
			delAll++
		}
	}
	return unsub, del, delAll
}

// FromDecisions builds a plan from the review's decisions and the sender
// groups they were made against, copying the unsubscribe method and URIs so
// apply can act without going back to the database.
func FromDecisions(runID, account string, messageCount int, groups []store.SenderGroup, decs []store.Decision) *Plan {
	byKey := make(map[string]store.SenderGroup, len(groups))
	var latest time.Time
	for _, g := range groups {
		byKey[g.SenderKey] = g
		if g.LastSeen.After(latest) {
			latest = g.LastSeen
		}
	}

	p := &Plan{
		RunID:     runID,
		Account:   account,
		CreatedAt: time.Now().UTC(),
		Snapshot: Snapshot{
			MessageCount: messageCount,
			LatestSeen:   latest,
		},
	}
	for _, d := range decs {
		g := byKey[d.SenderKey]
		display := g.Display
		if display == "" {
			display = g.Address
		}
		if display == "" {
			display = d.SenderKey
		}
		p.Decisions = append(p.Decisions, Entry{
			SenderKey:     d.SenderKey,
			Display:       display,
			Address:       g.Address,
			Unsubscribe:   d.Unsubscribe,
			DeleteMatched: d.DeleteMatched,
			DeleteAll:     d.DeleteAll,
			IncludeKept:   d.IncludeKept,
			Method:        g.Method,
			URIs:          g.LatestURIs,
			OneClick:      g.LatestOneClick,
		})
	}
	return p
}
