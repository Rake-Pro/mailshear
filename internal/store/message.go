package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type Message struct {
	AccountID    int64
	Folder       string
	UID          uint32
	GmMsgID      uint64
	MessageID    string
	SenderKey    string
	DomainKey    string
	FromDisplay  string
	FromAddress  string
	ListID       string
	HasUnsub     bool
	Method       string
	UnsubURIs    []string
	OneClick     bool
	Flags        []string
	InternalDate time.Time
	Size         int64
	Subject      string
	GmLabels     []string
	// Keep is the transactional category the subject matched (receipt,
	// order, booking, security, account, custom), or empty. A non-empty
	// value means apply refuses to delete the message.
	Keep string
}

func marshalStrings(v []string) (string, error) {
	if len(v) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStrings(s string) []string {
	if s == "" || s == "[]" {
		return nil
	}
	var v []string
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil
	}
	return v
}

func (s *Store) InsertMessages(ctx context.Context, msgs []Message) error {
	if len(msgs) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: insert messages: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT OR REPLACE INTO messages (
			account_id, folder, uid, gm_msgid, message_id, sender_key, domain_key,
			from_display, from_address, list_id, has_unsub, method, unsub_uris,
			one_click, flags, internal_date, size, subject, gm_labels, keep
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		return fmt.Errorf("store: insert messages: prepare: %w", err)
	}
	defer stmt.Close()

	for _, m := range msgs {
		uris, err := marshalStrings(m.UnsubURIs)
		if err != nil {
			return fmt.Errorf("store: insert message uid %d: marshal unsub_uris: %w", m.UID, err)
		}
		labels, err := marshalStrings(m.GmLabels)
		if err != nil {
			return fmt.Errorf("store: insert message uid %d: marshal gm_labels: %w", m.UID, err)
		}
		_, err = stmt.ExecContext(ctx,
			m.AccountID, m.Folder, int64(m.UID), int64(m.GmMsgID), m.MessageID, m.SenderKey, m.DomainKey,
			m.FromDisplay, m.FromAddress, m.ListID, boolToInt(m.HasUnsub), m.Method, uris,
			boolToInt(m.OneClick), strings.Join(m.Flags, " "), timeToText(m.InternalDate), m.Size, m.Subject, labels, m.Keep,
		)
		if err != nil {
			return fmt.Errorf("store: insert message uid %d: %w", m.UID, err)
		}
	}
	return tx.Commit()
}

const messageColumns = `account_id, folder, uid, gm_msgid, message_id, sender_key, domain_key,
	from_display, from_address, list_id, has_unsub, method, unsub_uris,
	one_click, flags, internal_date, size, subject, gm_labels, keep`

func scanMessage(rows *sql.Rows) (Message, error) {
	var m Message
	var uid, gmMsgID, hasUnsub, oneClick int64
	var uris, flags, labels string
	var internalDate sql.NullString
	err := rows.Scan(&m.AccountID, &m.Folder, &uid, &gmMsgID, &m.MessageID, &m.SenderKey, &m.DomainKey,
		&m.FromDisplay, &m.FromAddress, &m.ListID, &hasUnsub, &m.Method, &uris,
		&oneClick, &flags, &internalDate, &m.Size, &m.Subject, &labels, &m.Keep)
	if err != nil {
		return Message{}, err
	}
	m.UID = uint32(uid)
	m.GmMsgID = uint64(gmMsgID)
	m.HasUnsub = hasUnsub != 0
	m.OneClick = oneClick != 0
	m.UnsubURIs = unmarshalStrings(uris)
	m.GmLabels = unmarshalStrings(labels)
	m.Flags = strings.Fields(flags)
	m.InternalDate = textToTime(internalDate.String)
	return m, nil
}

// MessagesForSender returns the sender's cached messages, newest first. With
// matchedOnly set only the messages that carried List-Unsubscribe come back,
// which is the default delete scope; the full set is the delete_all scope.
func (s *Store) MessagesForSender(ctx context.Context, accountID int64, senderKey string, matchedOnly bool) ([]Message, error) {
	query := `SELECT ` + messageColumns + ` FROM messages WHERE account_id = ? AND sender_key = ?`
	if matchedOnly {
		query += ` AND has_unsub = 1`
	}
	query += ` ORDER BY internal_date DESC, folder, uid`

	rows, err := s.db.QueryContext(ctx, query, accountID, senderKey)
	if err != nil {
		return nil, fmt.Errorf("store: messages for sender %s: %w", senderKey, err)
	}
	defer rows.Close()

	var out []Message
	for rows.Next() {
		m, err := scanMessage(rows)
		if err != nil {
			return nil, fmt.Errorf("store: messages for sender %s: scan: %w", senderKey, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// DeleteMessageRows drops the cached rows for uids in folder. It is called
// after those messages were moved to Trash, so the local state stops
// claiming they are where they no longer are.
func (s *Store) DeleteMessageRows(ctx context.Context, accountID int64, folder string, uids []uint32) error {
	if len(uids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: delete message rows: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx,
		`DELETE FROM messages WHERE account_id = ? AND folder = ? AND uid = ?`)
	if err != nil {
		return fmt.Errorf("store: delete message rows: prepare: %w", err)
	}
	defer stmt.Close()

	for _, uid := range uids {
		if _, err := stmt.ExecContext(ctx, accountID, folder, int64(uid)); err != nil {
			return fmt.Errorf("store: delete message row uid %d: %w", uid, err)
		}
	}
	return tx.Commit()
}
