// Package store implements the local SQLite state for mailshear.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("store: mkdir %s: %w", dir, err)
	}

	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return nil, fmt.Errorf("store: create %s: %w", path, err)
		}
		f.Close()
	} else if err != nil {
		return nil, fmt.Errorf("store: stat %s: %w", path, err)
	} else if err := os.Chmod(path, 0600); err != nil {
		return nil, fmt.Errorf("store: chmod %s: %w", path, err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
		"PRAGMA secure_delete=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("store: %s: %w", pragma, err)
		}
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// timeToText renders t for storage. A zero time is stored as NULL so that SQL
// MIN/MAX skip it instead of returning the empty string.
func timeToText(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func textToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t.UTC()
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
