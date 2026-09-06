package apply

import (
	"context"
	"io"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"

	"github.com/Rake-Pro/mailshear/internal/imapx"
	"github.com/Rake-Pro/mailshear/internal/store"
)

const (
	testUser = "user"
	testPass = "pass"
)

type nopLogger struct{}

func (nopLogger) Printf(string, ...interface{}) {}

type testMsg struct {
	raw   string
	flags []imap.Flag
}

// startServer brings up an in-memory IMAP server with INBOX, Archive and
// Trash, appending msgs to INBOX in order so their UIDs are 1..len(msgs).
func startServer(t *testing.T, msgs []testMsg) string {
	t.Helper()

	mem := imapmemserver.New()
	user := imapmemserver.NewUser(testUser, testPass)
	for _, name := range []string{"INBOX", "Archive", "Trash"} {
		if err := user.Create(name, nil); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
	}
	mem.AddUser(user)

	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
		Logger:       nopLogger{},
		Caps: imap.CapSet{
			imap.CapIMAP4rev1: {},
			imap.CapIMAP4rev2: {},
		},
	})
	ln, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	addr := ln.Addr().String()
	c, err := imapclient.DialInsecure(addr, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if err := c.Login(testUser, testPass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	for _, m := range msgs {
		var opts *imap.AppendOptions
		if len(m.flags) > 0 {
			opts = &imap.AppendOptions{Flags: m.flags}
		}
		cmd := c.Append("INBOX", int64(len(m.raw)), opts)
		if _, err := io.WriteString(cmd, m.raw); err != nil {
			t.Fatalf("append write: %v", err)
		}
		if err := cmd.Close(); err != nil {
			t.Fatalf("append close: %v", err)
		}
		if _, err := cmd.Wait(); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return addr
}

func dialTest(t *testing.T, addr string) *imapx.Client {
	t.Helper()
	c, err := imapx.DialInsecureForTest(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func openStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "mailshear.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st, dir
}

// folderUIDs lists the UIDs currently in mailbox, over a fresh connection.
func folderUIDs(t *testing.T, addr, mailbox string) []uint32 {
	t.Helper()
	c, err := imapx.DialInsecureForTest(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.SelectReadOnly(context.Background(), mailbox); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	var uids []uint32
	if err := c.FetchHeaders(context.Background(), 1, 0, func(h imapx.Header) error {
		uids = append(uids, h.UID)
		return nil
	}); err != nil {
		t.Fatalf("fetch %s: %v", mailbox, err)
	}
	return uids
}

// folderMessageIDs lists the Message-IDs currently in mailbox.
func folderMessageIDs(t *testing.T, addr, mailbox string) map[string]bool {
	t.Helper()
	c, err := imapx.DialInsecureForTest(addr, testUser, testPass)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	if _, err := c.SelectReadOnly(context.Background(), mailbox); err != nil {
		t.Fatalf("select %s: %v", mailbox, err)
	}
	uids := folderUIDs(t, addr, mailbox)
	ids, err := c.FetchIdentity(context.Background(), uids)
	if err != nil {
		t.Fatalf("fetch message ids: %v", err)
	}
	out := map[string]bool{}
	for _, id := range ids {
		out[id.MessageID] = true
	}
	return out
}

func mustSelectUIDValidity(t *testing.T, cl *imapx.Client, folder string) uint32 {
	t.Helper()
	info, err := cl.SelectReadOnly(context.Background(), folder)
	if err != nil {
		t.Fatalf("select %s: %v", folder, err)
	}
	return info.UIDValidity
}

func ts(offset int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Hour)
}
