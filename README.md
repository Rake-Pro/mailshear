# mailshear

Local bulk unsubscribe and cleanup for IMAP mailboxes. Every hosted "mass unsubscribe" service works by taking access to your whole mailbox, and the free ones fund themselves by selling what they read. mailshear is a single binary that runs on your own machine and talks IMAP straight to your provider: no third party ever sees the mailbox, and nothing leaves the machine except the traffic to your mail server and the unsubscribe requests for the senders you picked. It is one interactive program with no subcommands, so `mailshear` in a terminal is the whole interface.

## Screens

| Screen | Recording |
|---|---|
| accounts | ![accounts](docs/demo/accounts.gif) |
| setup | ![setup](docs/demo/setup.gif) |
| scan | ![scan](docs/demo/scan.gif) |
| review | ![review](docs/demo/review.gif) |
| confirm and apply | ![confirm-apply](docs/demo/confirm-apply.gif) |
| keep | ![keep](docs/demo/keep.gif) |

- accounts: every configured mailbox with its provider, stored credential, last scan and message count; pick one, add one, re-authenticate, sign out or remove one.
- setup: the address, the provider settling on Gmail on its own, the auth method, and the app password.
- scan: connecting, the per-folder progress bar and counters, then the handover to the review table.
- review: the sender table in sections, marking a brand row, expanding the senders behind it, filtering, and the detail pane.
- confirm and apply: exactly what would happen per sender, the unsubscribe results streaming in, the delete bar and the follow-up list.
- keep: a mixed sender's receipts held back from a delete, and the one-row waiver that includes them.

The recordings run against a scripted fake backend with a synthetic mailbox: every address, path, run id and counter in them is made up.

## Install

| Goal | Command |
|---|---|
| `mailshear` on PATH, any OS | `go run ./tools/install` (adds the Go bin directory to PATH; `-no-add-to-path` opts out) |
| Binary in `bin/` on Linux or macOS | `./build.sh` (or `make build`) |
| Binary in `bin\` on Windows | `.\build.ps1` |
| No toolchain | download the archive for your OS from [Releases](../../releases), unpack it, put `mailshear` on your PATH |
| GoLand | the shared run configurations in `.run/`: `install mailshear`, and `mailshear` itself |

- Requires Go 1.27 to build. Dependencies are vendored, so the build needs no network.
- Release binaries are unsigned: macOS needs `xattr -d com.apple.quarantine ./mailshear`, and Windows SmartScreen warns once about an unrecognized publisher.
- Programs that were already running keep their old PATH, so restart the terminal (or GoLand) after installing.

## First run

- Run `mailshear` in a terminal. With no config it opens on the setup form; with more than one account configured it opens on the accounts screen.
- Enter your address, confirm the provider it detected, choose an app password or an OAuth sign-in, and enter the credential.
- It scans the mailbox (read-only), then hands over to the review table where you mark senders and apply.

## Providers and auth

| Provider | IMAP host | Auth | Notes |
|---|---|---|---|
| Gmail / Workspace | imap.gmail.com:993 | app password, OAuth | app passwords need 2-Step Verification; Workspace admins can disable IMAP or app passwords |
| Outlook / Hotmail / Microsoft 365 | outlook.office365.com:993 | OAuth only | Microsoft no longer accepts app passwords for IMAP; OAuth needs an Entra app registration |
| Fastmail | imap.fastmail.com:993 | app password | created in Fastmail settings |
| iCloud | imap.mail.me.com:993 | app password | app-specific password from appleid.apple.com |
| Proton Mail | via Proton Bridge, host and port you supply | app password | this build speaks implicit TLS only; Bridge defaults to STARTTLS and has to be reconfigured |
| Any other IMAP | `imap.<domain>` guessed, override it | app password, OAuth via `provider: custom` | works against any IMAP4rev1 or rev2 server |

- The provider is detected from the address domain first and its MX records second, so a custom domain on Workspace or Microsoft 365 is recognised rather than guessed at. Host, port, folders and SMTP come from the provider and can be overridden.
- SMTP is optional and only enables `mailto:` unsubscribes; without it those senders are reported as needing a manual unsubscribe.
- OAuth uses a client you create yourself, so no mailshear-owned client sits between you and the provider: [docs/oauth.md](docs/oauth.md) has the console steps.

## Safety and privacy

- The scan is read-only: folders are opened with EXAMINE and headers fetched with BODY.PEEK, so nothing is marked read and nothing on the server changes.
- Deleting means MOVE to Trash. mailshear never sets `\Deleted`, never expunges Trash, and never scans Trash, Spam, Drafts or Sent for senders to act on. Undo is the one thing that reads Trash: it moves messages back out of it.
- Only messages that carried a `List-Unsubscribe` header are in scope, unless a sender is explicitly escalated to "everything from this sender" on its own row.
- Receipts, bills, orders, bookings and security mail are never deleted whatever the sender, and flagged or replied-to messages are never moved. The keep rule is on by default and waived only per sender, deliberately.
- Protected senders (config list plus the ones marked in the app) are never unsubscribed or deleted.
- Every action, skip and failure is one JSON line in `<data-dir>/audit/<run-id>.jsonl`. Undo reads those lines back and moves the mail out of Trash; the window is your provider's Trash retention, 30 days on Gmail.
- Stored locally, nothing anywhere else: the config (`~/.config/mailshear/config.yaml` on Linux, `~/Library/Application Support/mailshear/` on macOS, `%AppData%\mailshear\` on Windows), and under the data directory (`~/.local/share/mailshear/`, `%LocalAppData%\mailshear\`) the SQLite database of scanned headers, the credential or OAuth token, plans, audit logs and exports. Files are mode 0600, directories 0700.
- No telemetry, no analytics, no update checks, no server component. The maintenance menu drops the cached message data and compacts the database whenever you want it gone.
- Filter spam, do not unsubscribe from it: an unsubscribe link in unsolicited mail confirms the address is live.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `mailshear is interactive; run it in a terminal` | it has no batch mode by design; run it in a terminal, not a pipe or a cron job |
| `terminal too small` | the UI needs 70x16 |
| Gmail rejects the password | app passwords need 2-Step Verification on; an account password never works over IMAP |
| `does not advertise AUTH=XOAUTH2`, or IMAP refuses the login | IMAP is off for the mailbox, or an admin disabled it |
| OAuth fails with `invalid_grant` after about a week | Google's External + Testing mode expires refresh tokens after 7 days; sign in again or publish your client |
| The scan takes minutes | a first scan of a large mailbox does; interrupting it is safe and the next run resumes from the saved cursor |
| A sender keeps arriving after an unsubscribe | it shows up in the still-sending section next run; escalate it there or filter it at the provider |
| Undo says messages are missing | Trash retention expired, or they were emptied out of Trash; those are gone for good |

## More

- [docs/design.md](docs/design.md) is the design document, including the safety model and the scan-to-apply pipeline.
- [docs/oauth.md](docs/oauth.md) is the OAuth client setup for Google and Microsoft.
- [docs/backlog.md](docs/backlog.md) is what is asked for and not scheduled.
- [docs/demo/README.md](docs/demo/README.md) regenerates the recordings above.

## License

MIT, see [LICENSE](LICENSE).
