package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type Folder struct {
	AccountID   int64
	Name        string
	UIDValidity uint32
	LastUID     uint32
	SpecialUse  string
}

func (s *Store) GetFolder(ctx context.Context, accountID int64, name string) (Folder, error) {
	var f Folder
	var uidValidity, lastUID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT account_id, name, uidvalidity, last_uid, special_use
		FROM folders WHERE account_id = ? AND name = ?
	`, accountID, name).Scan(&f.AccountID, &f.Name, &uidValidity, &lastUID, &f.SpecialUse)
	if errors.Is(err, sql.ErrNoRows) {
		return Folder{}, fmt.Errorf("store: folder %s: %w", name, ErrNotFound)
	}
	if err != nil {
		return Folder{}, fmt.Errorf("store: get folder %s: %w", name, err)
	}
	f.UIDValidity = uint32(uidValidity)
	f.LastUID = uint32(lastUID)
	return f, nil
}

func (s *Store) ResetFolder(ctx context.Context, accountID int64, name string, uidValidity uint32) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: reset folder %s: %w", name, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE account_id = ? AND folder = ?`, accountID, name); err != nil {
		return fmt.Errorf("store: reset folder %s: delete messages: %w", name, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO folders (account_id, name, uidvalidity, last_uid, special_use) VALUES (?, ?, ?, 0, '')
		ON CONFLICT(account_id, name) DO UPDATE SET uidvalidity = excluded.uidvalidity, last_uid = 0
	`, accountID, name, uidValidity)
	if err != nil {
		return fmt.Errorf("store: reset folder %s: upsert cursor: %w", name, err)
	}
	return tx.Commit()
}

func (s *Store) SetFolderCursor(ctx context.Context, accountID int64, name string, uidValidity, lastUID uint32) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO folders (account_id, name, uidvalidity, last_uid, special_use) VALUES (?, ?, ?, ?, '')
		ON CONFLICT(account_id, name) DO UPDATE SET uidvalidity = excluded.uidvalidity, last_uid = excluded.last_uid
	`, accountID, name, uidValidity, lastUID)
	if err != nil {
		return fmt.Errorf("store: set folder cursor %s: %w", name, err)
	}
	return nil
}
