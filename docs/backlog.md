# Backlog

Items the owner has asked for that are not scheduled yet. Newest first.

| Added | Item | Notes |
|---|---|---|
| 2026-09-06 | Recordings for the history and maintenance screens | Only the accounts screen was added to `docs/demo`; the run history and the maintenance menu have no GIF yet. `record.py` takes one function per recording. |
| 2026-09-06 | Drop the unreachable standalone review path | `Options.Embedded: false` is now only exercised by tests: the review screen is always one step of the flow. |
| 2026-09-06 | An account edit still drops an owned key the new account leaves empty | `UpsertAccount` now edits the account mapping in place, so comments inside the block survive. A key it owns that the new account no longer carries (`trash: ""` on an account with no override, the `oauth:` block after a switch back to a password) is still removed, and its comment goes with it. Dropping the stale `oauth:` block is the point; `trash: ""` is collateral. |
| 2026-09-06 | SMTP XOAUTH2 still ends the exchange on a challenge | `saslClient.Next` (IMAP) now answers a challenge with an empty response and lets the server's NO come back through the normal path, with the challenge text attached to the error. `smtpAuth.Next` still returns the error directly: `net/smtp` gives the mechanism no way to send the empty line and then read the failure. The challenge text is sanitized either way. |
| 2026-09-06 | Gmail X-GM-MSGID / X-GM-LABELS support | Blocked on go-imap/v2 (beta.8 has no Gmail extensions). Needed for label restore on undo and a second identity check before moves. Fork or upstream patch. |
| 2026-09-06 | Proton Bridge (STARTTLS on 1143) | Dial is implicit TLS only today. |
