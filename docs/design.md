# mailshear: local bulk unsubscribe and cleanup for IMAP mailboxes

Design document: what the tool is for, the invariants it holds to, and how the pieces fit together.

## 1. Problem and goals

A mailbox accumulates hundreds of newsletter and marketing subscriptions. Every hosted "mass unsubscribe" service solves this by taking OAuth access to the whole mailbox, and the free ones fund themselves by selling what they read (Unroll.me settled with the FTC over exactly this in 2019; Cleanfox and Edison were caught doing the same). The goal is a tool that does the same job with no third party involved: a single binary that runs on the user's own machine, talks IMAP directly to the mail provider, and never sends mailbox contents anywhere except back to that provider.

The tool is run by hand, periodically, not as a daemon. Each run scans for bulk senders, presents them in a terminal checklist where the user decides per sender whether to unsubscribe and whether to also purge that sender's existing mail, then applies those decisions.

Goals, in priority order: never touch mail the user did not explicitly select; never lose mail irrecoverably (trash, not expunge, with an undo path); work against Gmail and Workspace first but run against any IMAP server; be shareable with non-technical users, which means a config file and stored credentials rather than flags and environment gymnastics; and be fast enough on repeat runs that a monthly sweep takes a couple of minutes including the human review.

Non-goals: a GUI or web UI, running as a service, spam filtering, anything requiring provider APIs beyond IMAP and SMTP, multi-user or hosted operation, and AI classification of senders.

## 2. Shape of the program

Go, single static binary, cross-compiled for Linux, macOS and Windows. IMAP as the only mailbox protocol, with Gmail extensions used opportunistically when the server advertises them. A bubbletea terminal UI. SQLite (pure Go driver, no cgo) for local state so incremental scans and undo work across runs.

There are no subcommands. `mailshear` starts the UI and the UI is the whole interface; non-interactive use is dropped on purpose, because every dangerous step in this tool exists to be reviewed by a human. The only flags are `--config`, `--data-dir`, `-v`, `--version` and `-h`. Running without a terminal is a usage error rather than a batch mode.

Exit codes carry the one distinction worth scripting around: 4 when a session ends with senders that still need a manual unsubscribe, 2 for a usage error or no terminal, 1 for a failure.

Authentication is app passwords or XOAUTH2, both offered on the setup form and both switchable afterwards from the accounts screen. Neither is the fallback for the other in code; a Workspace admin can disable either path, so both stay first-class.

## 3. Pipeline overview

One session is three phases, run back to back.

**Scan** connects to the mailbox, fetches headers for messages not seen in a previous run, identifies messages that belong to a bulk sender, and aggregates them into sender groups stored in the local database. It changes nothing on the server.

**Review** opens the sender table over the current groups. The user toggles unsubscribe and delete per row, can protect a sender permanently, and on leaving the screen the decisions are written to a plan file. It changes nothing on the server.

**Apply** reads that plan, performs the unsubscribes, moves the selected messages to Trash, records every action in an audit log, and updates sender state so the next run knows what was already done.

Undo reverses a previous apply from its audit log, reached from the results screen or from the run history.

With more than one account configured the session starts on the accounts screen instead: every configured mailbox with its provider, stored credential, last scan and message count, and the actions to switch, add, re-authenticate, sign out or remove. With no config at all it starts on the setup form.

## 4. Identifying bulk mail

The central question is which messages are "subscriptions" and which are the receipts, statements and security alerts the user wants left alone. The tool does not try to understand content. It keys off structure.

A message is a bulk candidate if it carries a List-Unsubscribe header. Gmail and Yahoo have required RFC 8058 one-click unsubscribe headers from high-volume senders (5,000 or more messages a day to Gmail) since 2024, so any large legitimate sender has it, and transactional mail (order confirmations, 2FA codes, fraud alerts, statements) is exempt from that requirement and usually ships without it. This is a heuristic and not a guarantee, so the tool layers protections on top, but it is the reason the approach works at all: the tool never even considers a message that lacks the header, unless the user explicitly widens scope for a sender.

Header parsing has to handle the real-world mess: multiple URIs in angle brackets separated by commas, folded header lines, mailto: URIs with subject= and body= query parameters, bare URLs without brackets from sloppy senders, and RFC 2047 encoded display names in From. List-Unsubscribe-Post: List-Unsubscribe=One-Click alongside an https URI marks a sender as one-click capable. Each message is classified into one unsubscribe method: oneclick (RFC 8058 POST), http (an https link but no one-click header), mailto, or none (header present but unparseable).

Sender grouping uses a two-level key. The primary key is the List-Id value when present, since that is stable across rotating envelope and From addresses, and the normalized lowercase From address otherwise. A secondary key, the registrable domain of the From address computed with the public suffix list, is what the review screen groups rows by. Both keys are stored per message so the grouping can be changed at review time without rescanning.

Per group, scan records the message count, first and last seen dates, total size, the best available unsubscribe method across its messages, the most recent message's unsubscribe URIs (tokens in these links expire, so always act on the newest), a handful of recent subjects for display, which folders or Gmail labels the messages live in, and three safety counters: how many of the sender's messages in the mailbox lack a List-Unsubscribe header (a "mixed" sender like Amazon, whose marketing and receipts come from the same address), how many are flagged or starred, and how many the user has replied to.

## 5. Safety model

These are invariants, not options. Anything that would violate one is a bug.

Deletion means MOVE to the Trash folder. The tool never sets \Deleted and expunges, never empties Trash, and never touches Spam. On Gmail, Trash auto-purges after 30 days, which is the undo window; on other servers, the undo window is whatever the user's Trash policy is.

The delete scope for a sender is only the messages that matched as bulk candidates during scan, meaning only messages carrying List-Unsubscribe. For a mixed sender this leaves the receipts alone even when the user deletes the marketing. Deleting everything from a sender regardless of header is a separate, explicit per-row escalation, visually distinct, and it is blocked for any sender on the protected list.

Transactional mail is never deleted, whatever the sender. Absence of List-Unsubscribe is not enough on its own: PayPal, Chase and Amazon send marketing and receipts from the same address, and some of those receipts carry the header too. `internal/keep` holds a word-boundary phrase list in five categories (receipt, order, booking, security, account); `keep.Match(subject)` is the whole API, and `keep.Matcher` layers the user's `protect.keep_subjects` patterns and the `protect.keep_transactional` off switch, which defaults on. Scan classifies every message before the subject is dropped for non-bulk rows, so a mixed sender's receipts are recognisable later even though their subjects are not kept. Enforcement lives in apply, not in the UI: the identity check fetches the live Message-ID and Subject in the same BODY.PEEK round trip and skips any UID whose subject matches, with the reason `kept: <category>` in the audit log; the stored category is the fallback when the live subject does not match. The only waiver is `include_kept` on a plan entry, set from a review row, refused on protected senders and prompted for once per session.

Flagged, starred, and replied-to messages are never deleted, regardless of selection. Messages in Drafts and Sent are never touched.

Protected senders are never acted on. The protected list is a user-maintained set of addresses, domains and List-Ids in the config plus a table in the database that the review screen adds to. It ships with an empty default; the tool does not guess at what is a bank.

Apply refuses to run without a plan file produced by review (or hand-written in the same format), re-verifies UIDVALIDITY for every folder before acting and skips the folder if it changed, and shows a confirmation screen with exactly what is about to happen. Every action, including failures, is appended to a JSONL audit log with enough detail to reverse it: account, folder, UID, Message-ID, Gmail message ID and labels when available, destination folder, timestamp, and run ID.

No mailbox data leaves the machine except to the mail server itself and, for unsubscribe, to the sender's unsubscribe endpoint. No telemetry, no update checks.

## 6. Review screen

Built on bubbletea with lipgloss for styling: a table of sender rows with a detail pane below it and a status bar summarizing the pending plan.

Rows are brand-level by default. A flat per-sender list turns one intent into many decisions, because a large sender arrives as several List-Ids across several subdomains. A row is therefore one registrable domain, labelled with the display name most of its members use, then the domain and the sender count. Expanding a row lists its members in place: a decision on the row applies to every member, a member can be toggled on its own, and a row whose members disagree renders as partially selected. Filtering matches members, so searching for one subdomain keeps the brand row that carries it. Plan entries stay per sender key; nothing in the plan format or in apply knows about domains.

The table is partitioned into four sections, in this order: Protected, `Keep: receipts and security`, Still sending, and Bulk. Empty sections are omitted and a section header can be collapsed. The keep section is where the mixed-sender shape lands: any transactional category seen anywhere in the sender's mail, or a fifth of its bulk mail transactional while it also sends mail without the header. Select-all, deselect-all and clear are section-scoped, so a bulk selection cannot reach into Keep or Protected. Sorting reorders rows inside sections; the sections themselves never move.

What a row carries: unsubscribe and delete state, the sender or brand identity, message count, last seen, unsubscribe method, and compact indicators for mixed sender, flagged messages, replied-to messages, previously unsubscribed, and still sending after unsubscribe. Default sort is message count descending, which puts the highest-volume offenders on top. Rows decided in a previous run are hidden by default and revealed with a toggle. Senders unsubscribed in a previous run that have sent since are surfaced in the still-sending section, because those are the ones worth escalating to delete-all or a provider-side filter.

Beyond marking rows, the screen can: escalate a sender to everything-from-this-sender with a one-time prompt; waive the transactional keep rule for one sender, also prompted; protect a sender permanently, which clears any selection on the row; filter by substring across sender, domain and subject; cycle the sort between count, last seen, size and sender; expand a detail pane with recent subjects, folders, size, unsubscribe URIs and the safety counters; open an unsubscribe URL in the system browser for manual handling; and leave without writing a plan.

The status bar shows how many senders are marked for unsubscribe, how many for delete, the total messages and bytes that would move to Trash, how many rows are held back by the keep rule, and how many selected senders have no automatable method and will be reported as manual.

Delete does not require unsubscribe on the same row. A user may have already unsubscribed elsewhere and just want the backlog gone.

Text input is paste-aware everywhere it exists, which matters because app passwords are 16 to 20 characters and come out of a password manager. Bracketed paste arrives as a paste message and is routed to the setup form or to the review filter; a key press carrying more than one rune is inserted whole, which covers terminals that deliver a paste as a burst of key presses. Control characters are dropped rather than stored, so the newline a password manager appends never reaches the value.

## 7. Apply

Unsubscribe is attempted using the most recent message's headers, in this order of preference.

**One-click**: HTTPS POST to the URI with body List-Unsubscribe=One-Click, content type application/x-www-form-urlencoded (the RFC recommends multipart but urlencoded is universally accepted and simpler), a descriptive User-Agent, a 15 second timeout, and no redirect following since the RFC forbids senders from redirecting a one-click POST. A 2xx is success. RFC 8058 also says receivers should check DKIM alignment before honoring one-click; the tool skips this because the user has already hand-selected the sender as legitimate.

**HTTP link without one-click**: a GET with redirects followed, up to a limit. Success is a 2xx, but this is recorded as probable, since many of these land on a page that requires a further click. The final URL is stored so the user can open it. Address-confirmation risk is not a concern here for the same reason as above: these are senders the user chose, not unknown spam. For unknown spam the right move is to filter, not to unsubscribe, and the README says so.

**Mailto**: send a message via SMTP to the address in the URI with the subject and body parameters if given, otherwise a subject of unsubscribe. This requires SMTP configuration. If SMTP is not configured, the row is reported as manual rather than failing the run.

Requests are rate limited to one per second per destination host and four in flight overall. Results are recorded per sender as ok, probable, manual, or failed with the HTTP status or error, and are shown in a summary at the end.

Deletion runs after unsubscribes. For each selected sender, the UIDs recorded at scan time are grouped by folder, filtered by the safety rules (drop flagged, replied, kept, and anything whose Message-ID or Gmail message ID no longer resolves to the recorded UID), and moved in batches of a few hundred UIDs using UID MOVE when the server advertises the MOVE capability, falling back to UID COPY followed by \Deleted and UID EXPUNGE only when it does not. The Trash folder is found via RFC 6154 special-use attributes, with a config override.

After apply, sender state is updated with the action taken, the timestamp, the run ID, and the last seen UID, so that subsequent scans can detect "still sending".

## 8. Provider specifics

**Detection.** The provider comes from the address domain first and its MX records second, so a custom domain on Workspace or Microsoft 365 is recognised rather than guessed at. `config.Defaults(Provider)` is the single source of the per-provider host, port, folders and SMTP, read by `Detect`, `DetectFromDomain`, `SMTPDefault` and `EffectiveFolders`, so there is one table to change. `config.DetectByMX` classifies a domain by its MX records with a three-second deadline; the classification is a pure function over one hostname, so it is tested without a resolver. The setup form has a provider picker between the address and the credential: auto-detect, the known providers, or Other IMAP. The MX lookup runs as a command so the update loop never waits on DNS, and an MX lookup that matches nothing opens the advanced fields.

**Gmail auth.** An app password requires 2-step verification on the account and IMAP enabled in Gmail settings. Workspace admins can disable both; the tool detects the resulting login failure and points at the settings rather than surfacing a raw IMAP error.

**OAuth.** XOAUTH2 covers Gmail, Workspace, Outlook and Microsoft 365 over the go-sasl abstraction. Where the OAuth client comes from was settled by not shipping one: `auth: oauth` takes a client ID (and, for a Google desktop client, a secret) the user creates in their own Google Cloud or Entra project, so no mailshear-owned client sits between the user and the provider and there is nothing to get verified, rate limited or revoked centrally. `internal/oauth` implements the authorization-code flow with PKCE (S256) and a 127.0.0.1 loopback redirect on the standard library alone; tokens live at `<data-dir>/credentials/<account>.oauth.json`, mode 0600, and are refreshed on use. Sign-in is reached from the setup form on first run and from the accounts screen afterwards for re-auth and sign-out. See [oauth.md](oauth.md).

**Gmail scanning.** Scanning [Gmail]/All Mail covers every label in one pass, so on Gmail the folder list defaults to just that; Trash and Spam are excluded automatically because All Mail does not include them. When the server advertises X-GM-EXT-1, scan also fetches X-GM-MSGID (stable across label changes, used as the identity check before deletion and for undo) and X-GM-LABELS (shown in the UI so the user can see a sender is landing in Promotions, and recorded so undo can restore labels). X-GM-RAW can be used as an optional pre-filter to speed up a first scan on very large mailboxes, but the default is a full header sweep because the categories are not reliable enough to trust for exclusion.

**Gmail deletion.** Deletion on Gmail must be UID MOVE to [Gmail]/Trash. \Deleted plus EXPUNGE is not equivalent: Gmail's IMAP settings include an option for what happens when a message is expunged from its last visible folder, and the default archives rather than trashes, so the same command means different things on different accounts. MOVE to Trash is unambiguous. Gmail limits simultaneous IMAP connections per account to 15 and has a daily bandwidth cap that headers-only fetches will not approach; scan uses one connection and apply at most two.

**Known gaps.** go-imap/v2 is still at a beta release and carries no X-GM extension support, so there is no `X-GM-LABELS` restore on undo and no `X-GM-MSGID` identity check before deletion on Gmail; undo moves messages back out of Trash and prints a notice when it detects Gmail. Proton Bridge defaults to STARTTLS, which this build does not implement (implicit TLS only), so Proton accounts need Bridge reconfigured for implicit TLS and the host and port entered by hand on the setup form.

## 9. Scan mechanics and performance

Per folder, the database stores UIDVALIDITY and the highest UID processed. A scan fetches only UIDs above the cursor, in chunks of about 500, with UID FETCH n:m (UID INTERNALDATE RFC822.SIZE FLAGS BODY.PEEK[HEADER.FIELDS (FROM SENDER LIST-ID LIST-UNSUBSCRIBE LIST-UNSUBSCRIBE-POST PRECEDENCE SUBJECT MESSAGE-ID DATE)]) plus the Gmail attributes when available. BODY.PEEK matters: a plain BODY[] fetch would set \Seen on every unread message in the mailbox. The cursor is persisted after every chunk so an interrupted first scan resumes rather than restarts.

Messages without List-Unsubscribe are still counted per sender (for the mixed-sender indicator) but only their sender key, UID, folder, flags and keep category are stored, not subjects. Messages with the header store the parsed unsubscribe data and a truncated subject.

A first scan of a 200k-message mailbox is on the order of 100 MB of header traffic and completes in a few minutes over a decent connection, with a progress bar. Repeat scans touch only new mail and take seconds. Flags can change on old messages (a user stars something after the scan), so apply re-fetches FLAGS for the UIDs it is about to move rather than trusting the scan-time values.

If UIDVALIDITY changes for a folder, that folder's message records are discarded and it is rescanned from zero. Sender decisions and history survive since they are keyed by sender, not UID.

## 10. Local state

Everything lives under the XDG data directory (~/.local/share/mailshear/ on Linux, the platform equivalent elsewhere), with config under the XDG config directory.

SQLite schema, abbreviated:

```
accounts   (id, name, host, username)
folders    (account_id, name, uidvalidity, last_uid, special_use)
messages   (account_id, folder, uid, gm_msgid, message_id, sender_key,
            domain_key, has_unsub, method, unsub_uris, flags,
            internal_date, size, subject, gm_labels, keep)
senders    (account_id, sender_key, display, domain_key, list_id,
            protected, first_seen, last_seen, decision, decided_at,
            unsub_status, unsub_at, unsub_run_id, last_uid_at_decision)
runs       (id, account_id, started_at, finished_at, plan_path, summary)
```

The audit log is audit/<run-id>.jsonl beside the database, append-only, one line per action. The plan file is YAML, written to plans/<run-id>.yaml, containing the account, the list of sender decisions (unsubscribe, delete_matched, delete_all, include_kept), and the scan snapshot the plan was built against so apply can warn if the database moved on underneath it.

Subjects and addresses are personal data even on the user's own disk. The maintenance menu's purge action drops message rows and subjects while keeping sender decisions and history, and compacts the database. The database file is created with mode 0600, its directory 0700.

## 11. Configuration

YAML, one file, multiple accounts allowed.

```yaml
accounts:
  - name: personal
    host: imap.gmail.com
    port: 993
    username: you@gmail.com
    auth: file                      # file | oauth | env:VARNAME | file:/path
    folders: ["[Gmail]/All Mail"]   # default chosen by provider detection
    trash: ""                       # override; empty = discover via special-use
    smtp:                           # optional, enables mailto unsubscribes
      host: smtp.gmail.com
      port: 587
    # oauth:                        # with auth: oauth, see docs/oauth.md
    #   provider: google            # google | microsoft | custom
    #   client_id: ""
    #   client_secret: ""           # Google desktop clients issue one; empty for Microsoft
    #   tenant: common              # microsoft only
    #   auth_url: ""                # custom only
    #   token_url: ""
    #   scopes: []

protect:
  domains: ["chase.com", "fidelity.com", "irs.gov"]
  addresses: ["alerts@mybank.example"]
  list_ids: []
  keep_transactional: true          # the receipts-and-security rule; on by default
  keep_subjects: []                 # extra patterns to hold back

unsubscribe:
  http_get: true          # attempt plain https links, not just one-click
  follow_redirects: 5
  timeout_seconds: 15
  per_host_rps: 1

scan:
  gmail_prefilter: ""     # optional X-GM-RAW query, e.g. "category:promotions"
```

Credentials go in a small file store at <data-dir>/credentials/<account-name> (directory mode 0700, file mode 0600), written by the setup form. The env and file options remain available for cases where a plain file on disk is not wanted. Passwords never appear in the config file or in logs. An account on `auth: oauth` stores a token at <data-dir>/credentials/<account-name>.oauth.json instead, same modes, written atomically; the `oauth:` block holds only the client ID, the optional client secret and the provider selection, and access and refresh tokens never appear in the config file or in logs.

Accounts are added, edited and removed from the accounts screen. Writing one back is a yaml.Node upsert rather than a re-render of the file, so a user's comments, key order and other accounts survive an edit; only a config that does not exist yet is written from the template.

## 12. Project layout and dependencies

```
cmd/mailshear/        main, flag parsing, terminal check
internal/config/      YAML loading, validation, provider detection
internal/creds/       credential file store, OAuth token store
internal/imapx/       connection, capability detection, fetch/move helpers, Gmail extensions
internal/scan/        incremental fetch, header parsing, grouping
internal/headers/     List-Unsubscribe parsing (pure functions, heavily tested)
internal/keep/        transactional-subject classification
internal/oauth/       authorization-code flow with PKCE, XOAUTH2
internal/store/       SQLite schema, migrations, queries
internal/plan/        plan file format
internal/tui/         bubbletea models, views, key handling
internal/unsub/       one-click, http, mailto executors, rate limiting
internal/apply/       orchestration, safety filters, audit log
internal/undo/        reversal from the audit log
```

Libraries: emersion/go-imap/v2 for IMAP (MOVE, special-use, and a clean fetch API), emersion/go-sasl for XOAUTH2, charmbracelet bubbletea, bubbles and lipgloss for the UI, modernc.org/sqlite for cgo-free SQLite, the standard library's flag package, go.yaml.in/yaml/v3 for config, golang.org/x/net/publicsuffix for domain grouping, rs/zerolog for logging, and the standard library's net/mail, mime, net/smtp and net/http. Dependencies are vendored. Release with goreleaser producing static binaries for linux/amd64, linux/arm64, darwin/arm64, darwin/amd64 and windows/amd64.

## 13. Testing

internal/headers gets table-driven tests against a corpus of real List-Unsubscribe headers collected from a variety of senders (Mailchimp, SendGrid, Substack, Amazon SES, Google Groups, hand-rolled), including folded lines, multiple URIs, mailto with encoded parameters, and malformed cases.

Grouping, the keep rule and safety filtering are tested against synthetic message sets covering mixed senders, flagged messages, and List-Id versus From keying. Apply and undo run against an in-memory harness that asserts on the exact IMAP operations issued.

The IMAP layer is tested against go-imap/v2's in-memory imapserver for the generic path. Unsubscribe executors are tested against httptest servers that assert on method, body, content type, redirect behavior and header injection through mailto parameters. The UI is tested by driving the models directly and asserting on rendered frames.

Before first real use against a mailbox that matters, the recommended manual test is a run that unsubscribes but deletes nothing, then a second run deleting a single low-value sender, then undo, confirming the messages come back.

## 14. Open questions

Whether to include a filter action alongside unsubscribe and delete that, on Gmail, creates a server-side filter to auto-trash a sender who keeps sending. It is the natural escalation for still-sending rows but needs the Gmail API rather than IMAP. For now the manual filter is documented as the workaround.

Whether the delete scope should offer a date cutoff ("delete matched messages older than 90 days") so a user can keep recent issues of a newsletter while clearing the backlog. Cheap to add to the plan format; the affordance in the review screen is the question.

Whether one-click should use multipart form encoding per the RFC's recommendation, or urlencoded. Currently urlencoded, watching for senders that reject it.

Whether STARTTLS is worth implementing for the IMAP connection, which would let Proton Bridge work without reconfiguration.
