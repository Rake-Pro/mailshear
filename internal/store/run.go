package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Run is one apply invocation: the plan it executed and how it ended.
type Run struct {
	ID         string
	AccountID  int64
	StartedAt  time.Time
	FinishedAt time.Time
	PlanPath   string
	Summary    string
}

// timeToCol renders t for a NOT NULL text column, where the empty string
// stands in for "not yet".
func timeToCol(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func (s *Store) InsertRun(ctx context.Context, r Run) error {
	if r.ID == "" {
		return fmt.Errorf("store: insert run: empty run id")
	}
	started := r.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (id, account_id, started_at, finished_at, plan_path, summary)
		VALUES (?, ?, ?, ?, ?, ?)
	`, r.ID, r.AccountID, timeToCol(started), timeToCol(r.FinishedAt), r.PlanPath, r.Summary)
	if err != nil {
		return fmt.Errorf("store: insert run %s: %w", r.ID, err)
	}
	return nil
}

func (s *Store) FinishRun(ctx context.Context, runID, summary string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE runs SET finished_at = ?, summary = ? WHERE id = ?`,
		timeToCol(time.Now()), summary, runID)
	if err != nil {
		return fmt.Errorf("store: finish run %s: %w", runID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: finish run %s: %w", runID, err)
	}
	if n == 0 {
		return fmt.Errorf("store: run %s: %w", runID, ErrNotFound)
	}
	return nil
}

func (s *Store) GetRun(ctx context.Context, runID string) (Run, error) {
	var r Run
	var started, finished string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, account_id, started_at, finished_at, plan_path, summary FROM runs WHERE id = ?`, runID).
		Scan(&r.ID, &r.AccountID, &started, &finished, &r.PlanPath, &r.Summary)
	if errors.Is(err, sql.ErrNoRows) {
		return Run{}, fmt.Errorf("store: run %s: %w", runID, ErrNotFound)
	}
	if err != nil {
		return Run{}, fmt.Errorf("store: get run %s: %w", runID, err)
	}
	r.StartedAt = textToTime(started)
	r.FinishedAt = textToTime(finished)
	return r, nil
}

// ListRuns returns the account's runs, newest first.
func (s *Store) ListRuns(ctx context.Context, accountID int64) ([]Run, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, account_id, started_at, finished_at, plan_path, summary
		FROM runs WHERE account_id = ? ORDER BY started_at DESC, id DESC
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: list runs: %w", err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		var r Run
		var started, finished string
		if err := rows.Scan(&r.ID, &r.AccountID, &started, &finished, &r.PlanPath, &r.Summary); err != nil {
			return nil, fmt.Errorf("store: list runs: scan: %w", err)
		}
		r.StartedAt = textToTime(started)
		r.FinishedAt = textToTime(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}
