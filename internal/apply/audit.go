package apply

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Actions recorded in the audit log.
const (
	actionUnsubscribe = "unsubscribe"
	actionMove        = "move"
	actionSkip        = "skip"
	actionError       = "error"
	actionUndo        = "undo"
)

// Statuses on a "move" line. A move is recorded twice: an intent line per UID
// before the IMAP MOVE, and one confirmation line per batch after it. A UID
// with an intent line and no confirmation may or may not have moved, so undo
// still looks for it.
const (
	statusPending = "pending"
	statusDone    = "done"
)

// runIDRe matches a run id as produced by plan.NewRunID. Run ids index into
// the audit and plan directories, so anything else is rejected rather than
// joined onto a path.
var runIDRe = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}-[0-9a-f]{4}$`)

// ValidRunID reports whether s is a well-formed run id.
func ValidRunID(s string) bool { return runIDRe.MatchString(s) }

// Line is one audit record. Only the fields relevant to the action are set;
// undo reads back the "move" lines.
type Line struct {
	TS         time.Time `json:"ts"`
	RunID      string    `json:"run_id"`
	Account    string    `json:"account"`
	Action     string    `json:"action"`
	SenderKey  string    `json:"sender_key,omitempty"`
	Folder     string    `json:"folder,omitempty"`
	UID        uint32    `json:"uid,omitempty"`
	UIDs       []uint32  `json:"uids,omitempty"`
	MessageID  string    `json:"message_id,omitempty"`
	GmMsgID    uint64    `json:"gm_msgid,omitempty"`
	From       string    `json:"from,omitempty"`
	To         string    `json:"to,omitempty"`
	Method     string    `json:"method,omitempty"`
	URI        string    `json:"uri,omitempty"`
	FinalURL   string    `json:"final_url,omitempty"`
	Status     string    `json:"status,omitempty"`
	HTTPStatus int       `json:"http_status,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// AuditPath is where the audit log for runID lives under dataDir.
func AuditPath(dataDir, runID string) string {
	return filepath.Join(dataDir, "audit", runID+".jsonl")
}

// Auditor appends JSONL records to a run's audit log. The first write error
// is remembered so callers can stop before doing anything else irreversible.
type Auditor struct {
	f       *os.File
	enc     *json.Encoder
	runID   string
	account string
	err     error
}

// OpenAudit creates or extends the audit log for runID, with the directory at
// mode 0700 and the file at 0600.
func OpenAudit(dataDir, runID, account string) (*Auditor, error) {
	path := AuditPath(dataDir, runID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("apply: mkdir %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("apply: open audit log %s: %w", path, err)
	}
	return &Auditor{f: f, enc: json.NewEncoder(f), runID: runID, account: account}, nil
}

func (a *Auditor) Path() string { return a.f.Name() }

// Write appends one line. Errors are recorded rather than returned so the
// call sites stay readable; check Err before the next irreversible step.
func (a *Auditor) Write(l Line) {
	l.TS = time.Now().UTC()
	l.RunID = a.runID
	l.Account = a.account
	if err := a.enc.Encode(&l); err != nil && a.err == nil {
		a.err = fmt.Errorf("apply: writing audit log %s: %w", a.f.Name(), err)
	}
}

func (a *Auditor) Err() error { return a.err }

// Sync flushes the log to disk and reports the first write error. Call it
// after writing the intent lines for an irreversible step and before taking
// that step: if the record cannot be made durable, the step must not happen.
func (a *Auditor) Sync() error {
	if a.err != nil {
		return a.err
	}
	if err := a.f.Sync(); err != nil {
		a.err = fmt.Errorf("apply: syncing audit log %s: %w", a.f.Name(), err)
		return a.err
	}
	return nil
}

func (a *Auditor) Close() error {
	err := a.f.Close()
	if a.err != nil {
		return a.err
	}
	return err
}

// ReadLines loads an audit log written by a previous run.
func ReadLines(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("apply: reading audit log %s: %w", path, err)
	}
	defer f.Close()

	var out []Line
	dec := json.NewDecoder(f)
	for dec.More() {
		var l Line
		if err := dec.Decode(&l); err != nil {
			return nil, fmt.Errorf("apply: parsing audit log %s: %w", path, err)
		}
		out = append(out, l)
	}
	return out, nil
}
