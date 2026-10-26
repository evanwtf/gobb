# gobb Plan 4c: Password reset by email — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A member who forgets a password resets it without the administrator: the sign-in page links to a form, gobb emails a link, and the link opens a page that sets a new password.

**Architecture:**
- **Settings.** `internal/config` reads the SMTP server, the sender address and the public URL of the site. Email is off when no SMTP server is set. Then the reset pages do not exist and the sign-in page has no link.
- **Sending.** A new `internal/mail` package builds each message and sends it over SMTP with TLS. A queue in the database holds every email. A worker in `gobb serve` sends the queue with retries. No request waits on SMTP.
- **Reset.** `internal/board` makes the links, stores only their SHA-256, and sets the new password. Each link is single-use, lasts 1 hour, and dies when the password changes by any other way.
- **Pages.** `internal/web` adds the request form and the new-password page. Each POST handler calls one board function.

**Tech Stack:** Go 1.27 standard library (`net/smtp`, `crypto/tls`, `net/mail`, `mime`, `mime/quotedprintable`); pgx v5.

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md`: section 7 ("Login": "Login and password-reset responses do not reveal whether an account exists"; "Sessions": "a password reset … ends all of that user's sessions"), "Email" ("SMTP with STARTTLS or implicit TLS, and optional auth"; "A queue in the database with retries and backoff. A failed send never blocks a request"; "reset links last 1 hour"), and section 17. The owner moved password reset by email into release 1 on 2026-10-25: it blocks the cutover. Roadmap row 4c.

**Workers:** Sonnet 5.5, high effort; the controller sets the model and the effort on every dispatch. Reviewer: Opus, high effort, after every task. After Task 4, a separate Opus security review covers Tasks 1–4. An Opus review of the whole plan comes at the end.

## Global Constraints

- **Clean room.** Work only from this repo and public standards: RFCs (5321 SMTP, 5322 messages, 3207 STARTTLS, 4616 PLAIN, 8314 implicit TLS, 2045–2047 MIME, 3834 Auto-Submitted, 2606 example names), and the Go and PostgreSQL docs. Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. Name no other forum software in code, docs, tests or commits. (`AGENTS.md`; spec 1.)
- **Real data stays with the controller.** Workers use `bundletest` fixtures and made-up data only.
- **This repo is public.** Never commit hostnames, IP addresses other than `127.0.0.1`, `10.x` and `2001:db8::/32`, home-directory paths, secret values, secret-manager item names, the production sending address, or forum data. Examples use the names of RFC 2606: `board@example.org`, `smtp.example.org`, `https://board.example.org/forums/`.
- **No new dependency.** Nothing in `go.mod` changes.
- **Commits.**
  - Subjects are `area: imperative summary`.
  - No agent session URLs or IDs, and no agent trailers.
  - Work lands on `main`. Push after every task.
  - Each task adds its bullet to `docs/changelog.md` under a new `## 0.3.0 (unreleased)` section.
- **PostgreSQL in tests.** Tests that need it use `dbtest.New` (through `boardtest.Load`) and skip when `GOBB_TEST_DATABASE_URL` is unset. CI sets it and fails on any skip.
- **Migrations.**
  - Never edit a migration that is already on `main`.
  - This plan adds `0014_password_reset.sql` (Task 2) and nothing else.
- **Secrets stay out of logs and pages.** The SMTP password, a reset token, a reset link, an email body and a recipient address go into no log line, no error that reaches a log or a page, and no `mod_log` row. A log line names the queue row ID and the user ID. An SMTP error goes to the log and to `last_error` as a summary only (Task 1, `mail.Summary`), because a server's reply can repeat the address.
- **Rights are checked in `internal/board`, not only in `internal/web`.** A reset goes only to an account that may change its password (`account.password`).
- **GET and HEAD change nothing.** Opening a reset link does not use it up: mail scanners open links.
- **Every change is a POST through the guard** (Cross-Origin Protection and the CSRF token). Guests use the guest token, as on the sign-in form.
- **No inline script, no `style` attributes and no `<style>` elements** in templates.
- **Email is off unless set.** With no `GOBB_SMTP_HOST`, the reset routes answer 404, the sign-in page shows no link, and the queue worker does not start.
- **A request never waits on SMTP.** Only the queue worker talks to the SMTP server, and it holds no database connection while it does.

## Review Focus

1. **A reset link that points at another host.** An attacker sends `Host: evil.example` or `X-Forwarded-Host` with a reset request for a victim's name. The emailed link must use `GOBB_PUBLIC_URL` and nothing from the request. Test: `TestResetLinkIgnoresHost` (Task 4).
2. **A mail scanner that opens the link first.** Many mail services fetch each link in an email before the member sees it. A GET or HEAD of the link must leave it usable. Test: `TestResetGetDoesNotConsume` (Task 4).
3. **A server that drops STARTTLS.** A network attacker removes STARTTLS from the server's EHLO reply. gobb must stop before AUTH and before MAIL, and must not send the password or the message in clear. Test: `TestSendRefusesNoStartTLS` (Task 1).
4. **An answer that tells whether an account exists.** The request form gets an unknown name, an inactive account, a banned account, an account without `account.password`, and an account over its email limit. Each one gets the same status, the same `Location`, the same cookies and at least the same floor time as a real account. Test: `TestResetRequestSameAnswer` (Task 4).
5. **An SMTP outage.** The server is down for an hour. The request answers at once, the queue retries with backoff, and it drops the email (and clears its body) once the link has expired. Test: `TestOutageRetriesThenDrops` (Task 2).

---

## Decisions this plan fixes

**Settings.** Each one is an environment variable. An empty value counts as unset. `compose.yaml` passes each one through with `${NAME:-}`.

| Variable | Rule |
|---|---|
| `GOBB_SMTP_HOST` | Turns email on. Unset: email is off, and setting any variable below is an error that names it. |
| `GOBB_SMTP_PORT` | 1–65535. Default 587 for `starttls` and 465 for `implicit`. |
| `GOBB_SMTP_TLS` | `starttls` (the default) or `implicit`. There is no plain mode. |
| `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD` | Both or neither. AUTH PLAIN, only after TLS. |
| `GOBB_MAIL_FROM` | Required with a host. One plain address (no display name), as `net/mail` reads it. |
| `GOBB_PUBLIC_URL` | Required with a host. The site's address for links in email: `https`, a host, no user information, query or fragment, and a path equal to `GOBB_BASE_PATH`. `http` passes only when `GOBB_COOKIE_SECURE=false`. Stored with a trailing `/`. |

A config error names the variable and never holds the value of `GOBB_SMTP_PASSWORD`.

**URLs.** All are under the base path, and exist only when email is on.

| Path | What |
|---|---|
| `GET /reset` | the request form; `?sent=1` shows the "Check your email" notice instead |
| `POST /reset` | field `name`; answers 303 to `/reset?sent=1` |
| `GET /reset/{token}` | the new-password form, or 404 "This link has expired or has been used." |
| `POST /reset/{token}` | fields `password` and `again`; answers 303 to `/login?reset=1` |

**Who gets an email.** The name is looked up as at sign-in: by username first, then by email address (every account that shares it, at most 5). An account gets an email only when all of these are true. Otherwise the request does nothing, and the answer is the same.
- It is active.
- It has an address that `board.CleanEmailInput` accepts.
- No user ban and no email ban that has not ended holds it. (An address ban compares through `account.CleanEmail`, as sign-in does. A ban on the client's address does not stop a request: the member may ask from elsewhere.)
- It has `account.password` (board-wide).
- Its per-account limit has room.

`email_verified` does not matter: the link proves the address, so a completed reset sets it to true.

**Links.**
- A token is 32 random bytes in base64url without padding (43 characters), as a session token. The database holds only its SHA-256 in hex.
- A link is `GOBB_PUBLIC_URL + "reset/" + token`.
- A link lives 1 hour (`board.ResetLinkLife`).
- A link dies when its member's password epoch moves: a completed reset, a change on the account page, or a reset by the administrator. A completed reset also deletes every link of the member.
- A change of the member's email address deletes every link of the member.
- The new password follows `board.CheckNewPassword`: 10 to 1,024 bytes, at least 10 characters, valid UTF-8, and not the username.

**Limits.** Each count lives in memory in a sliding window, as in Plan 4.

| What | Limit | Over the limit |
|---|---|---|
| Requests (`POST /reset`) from one client address | 5 in an hour | 429 with `Retry-After`: "Too many requests from your address. Try again in N minutes." |
| Requests on the whole board | 30 in an hour | 429 with `Retry-After`: "The board is sending too many emails. Try again in N minutes." |
| Emails to one account | 3 in 24 hours | the usual 303; no email |
| Dead-link hits (GET or POST of a link that `board.ResetLink` refuses) from one address | 20 in an hour | 429 with `Retry-After` |
| Answer time of `POST /reset` | at least 400 ms from its start (`Options.ResetFloor`; tests set it lower) | — |

**The queue.**
- `email_queue` (migration 0001) gains `user_id` (cascade on delete), `send_by` and `dropped_at`.
- The worker wakes every 30 s, and at once when a request queues an email.
- One pass claims up to 10 due rows in one statement. It sets `next_attempt_at` 5 minutes ahead (a lease) and adds one to `attempts`, then commits. It sends each row with no transaction open, then writes the outcome.
- Success: `sent_at` = now and `body` = ''.
- A temporary failure (a network error, a timeout, a 4xx reply): `next_attempt_at` = now + 1 min, 5 min or 15 min after attempts 1, 2 and 3, and 30 min after later ones.
- A permanent failure (a 5xx reply), a passed `send_by`, or 8 attempts: `dropped_at` = now and `body` = ''.
- `last_error` holds `mail.Summary(err)`. Each failure and each drop also writes a WARN line with the row ID and the user ID. The spec puts failures in the admin log; the admin panel (roadmap row 6) reads `last_error`, and until it exists the server log is the place to look.
- The hourly cleanup deletes rows 30 days after `sent_at` or `dropped_at`, and every reset link past `expires_at`.

**Messages.** Plain text, UTF-8, quoted-printable, CRLF line ends. Headers: `From` (the plain `GOBB_MAIL_FROM`), `To`, `Subject` (RFC 2047 Q-encoding when not ASCII), `Date`, `Message-ID` (`<32 hex characters@the From domain>`), `MIME-Version: 1.0`, `Content-Type: text/plain; charset=utf-8`, `Content-Transfer-Encoding: quoted-printable`, `Auto-Submitted: auto-generated`. A CR or an LF in `To` or `Subject` is an error.

**The reset email.** `{board}` is the board name from the settings. `{name}` is the stored username.

Subject: `Reset your password on {board}`

```
Someone asked to reset the password of the account {name} on {board}.

To choose a new password, open this link within 1 hour:

{link}

The link works once. If you did not ask for this, ignore this email.
Your password stays the same.
```

**The test email** (`gobb mail test <address>`). Subject: `Test email from gobb`. Body: `This is a test email from gobb {version}. Email works.`

**Pages.** Each one carries `noindex` and `Cache-Control: no-store`. The answers of `/reset/{token}` also carry `Referrer-Policy: no-referrer`, because the token is in the path.
- **Sign-in page.** Under the button: `<p class="hint"><a href="{base}reset">Forgot your password?</a></p>`, only when email is on. With `?reset=1`, above the form: `<p class="notice" role="status">Your password is changed. Sign in with the new password.</p>`.
- **Request form.** Heading "Reset your password". Label "Username or email address". Hint: "We email a link to the address of the account. The link works for 1 hour." Button "Email me a link". An empty name: 400, "Enter your username or email address." beside the field.
- **Sent notice.** Heading "Check your email". Text: "If an account matches, we sent a link to its email address. The link works for 1 hour. If no email comes, look in your spam folder."
- **New-password form.** Heading "Choose a new password". Text: "For the account {name}." (in `<bdi>`). A hidden `username` field with `autocomplete="username"` and the stored username, for password managers. Fields "New password" (`password`, `autocomplete="new-password"`) and "Type it again" (`again`). Button "Save the password". Two different values: 400, "The two passwords differ." beside "Type it again". A refused password: 400 with the message of `board.CheckNewPassword` beside "New password". A refusal leaves the link usable.
- **Dead link.** 404 with the heading "Link not valid", the text "This link has expired or has been used.", and a link "Ask for a new link" to `/reset`.

---

### Task 1: Email settings and the SMTP sender

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `internal/mail/message.go`, `internal/mail/smtp.go`, `internal/mail/mail_test.go`, `internal/mail/mailtest/server.go`
- Create: `cmd/gobb/mail.go`, `cmd/gobb/mail_test.go`; Modify: `cmd/gobb/main.go` (the `mail` command)
- Modify: `compose.yaml`, `README.md` (a new "Email" section), `docs/changelog.md` (open `## 0.3.0 (unreleased)`)

**Interfaces:**
- Produces:
  - `config.Config.Mail *config.Mail`: nil when email is off.
  - `config.Mail{Host string; Port int; TLS string; User, Password string; From string; PublicURL string}`. It has `String()` and `LogValue()` that print every field except `Password`, which prints as `[set]` or `[unset]`.
  - `mail.Message{To, Subject, Body string}`.
  - `mail.Check(m mail.Message) error`: `To` is one plain address, and neither `To` nor `Subject` holds a CR or an LF.
  - `mail.Build(from string, m mail.Message, now time.Time) ([]byte, error)`: the whole message, headers and body, as the "Messages" decision says. It runs `Check` first.
  - `mail.Sender` interface: `Send(ctx context.Context, m mail.Message) error`.
  - `mail.NewSMTP(c config.Mail) *mail.SMTP`, which implements `Sender`. Its field `RootCAs *x509.CertPool` replaces the system roots when not nil (tests).
  - `mail.Permanent(err error) bool`: true for a 5xx SMTP reply.
  - `mail.Summary(err error) string`: `smtp 550 5.1.1` (the code, and the enhanced status code when the reply has one), `timeout`, `tls`, `no starttls` or `network`. It holds no text of the server's reply.
  - `mailtest.Start(t testing.TB, o mailtest.Options) *mailtest.Server`, a fake SMTP server on `127.0.0.1` for tests. `Options{TLS string; NoStartTLS bool; User, Password string; Reply map[string]int; Silent bool}`: `TLS` is `starttls` or `implicit`; `Reply` maps a command (`RCPT`, `DATA`) to a reply code; `Silent` accepts the connection and never answers. The server has `Port() int`, `CertPool() *x509.CertPool`, `Messages() []mailtest.Received` (`From`, `To`, `Data`), and `Log() []string` (each command it got, with `AUTH` shown as `AUTH PLAIN [hidden]`, and whether TLS was on at that point).

Rules for `SMTP.Send`:
- Dial with a 30 s timeout, and give the whole send a 30 s deadline (the context's, when it is sooner).
- `implicit`: TLS from the first byte. `starttls`: EHLO, then STARTTLS if the server offers it. If it does not, return an error that `Summary` reads as `no starttls`, and send nothing more but QUIT.
- TLS: `ServerName` = `Host`, at least TLS 1.2, `RootCAs` when set.
- EHLO with the host of `PublicURL`.
- AUTH PLAIN when `User` is set, only after TLS.
- MAIL FROM `From`, RCPT TO `m.To`, DATA with `mail.Build`, QUIT.
- An error never holds `Password`.

- [ ] **Step 1: Write the failing tests**
  - `TestMailFromEnv`, a table:
    - no `GOBB_SMTP_HOST` gives `Mail == nil`;
    - `GOBB_MAIL_FROM` set without a host is an error that names `GOBB_MAIL_FROM`;
    - with a host and `starttls`, the port defaults to 587; with `implicit`, to 465;
    - `GOBB_SMTP_TLS=none` is an error;
    - a user without a password is an error, and so is a password without a user;
    - `GOBB_MAIL_FROM="Board <board@example.org>"` is an error;
    - `GOBB_PUBLIC_URL=http://board.example.org/` is an error with secure cookies, and passes with `GOBB_COOKIE_SECURE=false`;
    - with `GOBB_BASE_PATH=/forums/`, `https://board.example.org/x/` is an error, and `https://board.example.org/forums` passes and is stored as `https://board.example.org/forums/`;
    - a URL with `?a=1`, `#f` or `user@` is an error;
    - with `GOBB_SMTP_PASSWORD=s3cr3t-pw` set, no error text of any case holds `s3cr3t-pw`.
  - `TestMailConfigRedacts`: `fmt.Sprint(cfg.Mail)`, `fmt.Sprintf("%+v", *cfg.Mail)` and a slog line with the value hold no `s3cr3t-pw`.
  - `TestBuild`:
    - the headers of "Messages" are present;
    - the subject `Réinitialiser` comes out Q-encoded, and decodes back;
    - `To: "a@example.org\r\nBcc: b@example.org"` and a subject with `\n` are errors;
    - a body with a line of 2,000 characters gives no line over 78 octets;
    - a body with a line that is only `.` reaches `mailtest` and decodes to the same body.
  - `TestSendStartTLS`: user and password set. The server's log shows `STARTTLS` before `AUTH`, and TLS on for `AUTH`, `MAIL`, `RCPT` and `DATA`. One message arrives, with the right `From` and `To`.
  - `TestSendImplicitTLS`: one message arrives.
  - `TestSendRefusesNoStartTLS`: `NoStartTLS: true`. Send fails, `Summary` gives `no starttls`, and the server's log holds no `AUTH`, `MAIL` or `DATA`.
  - `TestSendUntrustedCert`: `RootCAs` left nil, so the fake server's certificate is not trusted. Send fails with `tls`, and the server's log holds no `AUTH`.
  - `TestSendPermanent`: `RCPT` answers 550, so `Permanent` is true and `Summary` starts with `smtp 550`. `RCPT` answers 451, so `Permanent` is false.
  - `TestSendTimeout`: `Silent: true` and a context of 200 ms. Send returns within 1 s with `timeout`.
  - `TestMailTestCommand` (`cmd/gobb`):
    - with the environment set to the fake server, `gobb mail test someone@example.org` exits 0. One message arrives with the test subject. Stderr holds neither the password nor the address;
    - with no `GOBB_SMTP_HOST`, the command exits 1 with `email is not configured: GOBB_SMTP_HOST is unset`;
    - with no address, it exits 2 with the usage line.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/config/ ./internal/mail/... ./cmd/gobb/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
  - Write the settings, `internal/mail`, `mailtest` and the `mail test` command. The fake server makes its certificate in memory, for the IP address `127.0.0.1`. `gobb mail test` sends at once, through `SMTP.Send`, not through the queue.
  - Add the seven variables to the `gobb` service in `compose.yaml`, each as `${NAME:-}`.
  - Write the README section "Email": the table of settings, the example values of RFC 2606, `docker compose exec -T gobb gobb mail test "$ADDRESS"`, and the rule that the password goes in `.env` and nowhere else.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "mail: SMTP settings, the sender and gobb mail test"`

**Checkpoint:** none. The real sending account is tested in Task 5.

---

### Task 2: The email queue

**Files:**
- Create: `internal/db/migrations/0014_password_reset.sql`, `internal/mail/queue.go`, `internal/mail/queue_test.go`
- Modify: `cmd/gobb/main.go` (start the worker in `serve`; the cleanup), `cmd/gobb/serve_test.go`, `cmd/gobb/cleanup_test.go`, `docs/changelog.md`

**Interfaces:**
- Consumes: `mail.Message`, `mail.Sender`, `mail.Check`, `mail.Permanent`, `mail.Summary` (Task 1).
- Produces:
  - Migration `0014_password_reset.sql`:
    - `email_queue` gains `user_id bigint null references users (id) on delete cascade deferrable initially immediate`, `send_by timestamptz null` and `dropped_at timestamptz null`, and a partial index on `next_attempt_at` where `sent_at is null and dropped_at is null`.
    - A new table `password_resets`:
      - `id text primary key` (the SHA-256 hex of the token);
      - `user_id bigint not null references users (id) on delete cascade deferrable initially immediate`;
      - `epoch bigint not null` (the member's `password_epoch` when the link was made);
      - `created_at timestamptz not null`;
      - `expires_at timestamptz not null`;
      - an index on `user_id`.
  - `mail.Enqueue(ctx context.Context, q db.Querier, m mail.Message, userID int64, sendBy, now time.Time) (int64, error)`. It refuses, before the insert, any message that `mail.Check` refuses.
  - `mail.Queue{Pool *pgxpool.Pool; Sender mail.Sender; Now func() time.Time; Wake <-chan struct{}}`:
    - `(*Queue).Process(ctx) (sent, failed, dropped int, err error)` runs one pass;
    - `(*Queue).Run(ctx)` runs passes until `ctx` ends: one at once, then one on each tick of 30 s or each value on `Wake`.
  - `mail.Cleanup(ctx context.Context, q db.Querier, now time.Time) (int64, error)`.
  - In `cmd/gobb`, `serve` builds the queue when `cfg.Mail != nil`, and keeps a `wake` channel with room for one value. Task 4 hands its non-blocking send to the web layer. The worker stops before the pool closes, as the session cleanup does.

- [ ] **Step 1: Write the failing tests**
  - `TestMigration0014`: the columns and the table exist. A delete of a user deletes the user's queue rows and reset rows.
  - `TestEnqueueRefusesBadAddress`: `To: "a@example.org\r\nBcc: b@example.org"` returns an error, and no row exists.
  - `TestProcessSends`: one row, with the `mailtest` server. After `Process`, the row has `sent_at`, an empty `body` and `attempts = 1`, and the server holds one message.
  - `TestOutageRetriesThenDrops` (Review Focus 5): a `Sender` that always fails with a timeout. A row with `send_by` = now + 1 h. Passes at now, +1 min, +6 min and +21 min each fail, and set `next_attempt_at` 1, 5, 15 and 30 minutes ahead. A pass after `send_by` drops the row: `dropped_at` is set, `body` is empty, and the `Sender` was not called on that pass.
  - `TestProcessDropsPermanent`: a 550 drops the row at the first attempt, and `last_error` starts with `smtp 550`.
  - `TestProcessDropsAfterEight`: a row with no `send_by` and a failing `Sender` is dropped at the 8th attempt.
  - `TestProcessLease`: a `Sender` that blocks until the test frees it. A second `Process` that runs meanwhile claims nothing. After the free, the row was sent once.
  - `TestProcessHoldsNoConnection`: a pool of one connection and a `Sender` that blocks. While it blocks, the test runs `select 1` on the pool, and it succeeds.
  - `TestQueueLogsNoSecret`: capture slog through a send that fails with a reply that holds the address, and through one that succeeds. No line holds `@`, the subject or a body line.
  - `TestRunWakes`: `Run` with `Wake`. One value on `Wake` after an enqueue sends the row within 2 s.
  - `TestMailCleanup`: rows sent or dropped 31 days ago go. Rows of 29 days ago and pending rows stay.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/mail/ ./cmd/gobb/`. Expected: FAIL.
- [ ] **Step 3: Implement** the migration, the queue and the wiring in `serve`. Add `mail.Cleanup` to the hourly `cleanup`, and log the count it removed.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "mail: a queue in the database with retries, and its worker"`

**Checkpoint:** the controller starts the private copy with the new image of `main`. The migration applies, and the server starts. Email stays off.

---

### Task 3: Reset in the board

**Files:**
- Create: `internal/board/reset.go`, `internal/board/reset_test.go`
- Modify: `internal/board/account.go` (`changeEmail` deletes the links), `cmd/gobb/main.go` (the cleanup), `docs/changelog.md`

**Interfaces:**
- Consumes: `mail.Enqueue`, `mail.Message` (Tasks 1–2); `CheckNewPassword`, `CleanEmailInput`, `Member`, `LoadSettings`, `mayAccount`, `refuseBanned`, `lockUser`, `logMod`, `session.DeleteUser`, `password.Hash` (existing).
- Produces:
  - `board.ResetLinkLife = time.Hour`.
  - `type board.ResetMail func(username, token string) (subject, body string)`.
  - `board.RequestReset(ctx context.Context, pool *pgxpool.Pool, name string, allow func(userID int64) bool, mailFor board.ResetMail, now time.Time) (queued int, err error)`:
    - it follows "Who gets an email";
    - it calls `allow` only for an account that passes every other rule;
    - for each account it makes one link and queues one email, in one transaction: `send_by` = the link's `expires_at`, `user_id` = the account;
    - a name that is empty after trimming, not valid UTF-8, holds a NUL, or is over 512 bytes gives `0, nil`;
    - an error names no account and no address.
  - `type board.ResetAccount struct{ UserID int64; Username string }`.
  - `board.ResetLink(ctx context.Context, q db.Querier, token string, now time.Time) (board.ResetAccount, error)`: [ErrNotFound] for a token that is not 43 base64url characters, and for a link that is unknown, expired, of an older epoch, of an account that is not active or lacks `account.password`, or of a member whom a user ban holds. It writes nothing.
  - `board.CompleteReset(ctx context.Context, pool *pgxpool.Pool, token, next string, hold func(context.Context) (release func(), err error), now time.Time) (userID int64, err error)`:
    - it runs `ResetLink`, then `CheckNewPassword` for that member (an `*InputError` on the field `next`), then the hash under `hold`;
    - then one transaction: `lockUser`; the account still active; the link row `FOR UPDATE`, live and of the current epoch; `refuseBanned`. A failure of any of these is `ErrNotFound`, and writes nothing;
    - it stores the gobb hash, `password_scheme = 'gobb'`, the epoch plus one and `email_verified = true`;
    - it deletes every session and every link of the member;
    - it writes one `mod_log` row: `user.reset_password`, actor and target the member, details `{"via":"email"}`.
  - `board.CleanupResets(ctx context.Context, q db.Querier, now time.Time) (int64, error)` deletes links past `expires_at`.

- [ ] **Step 1: Write the failing tests** (fixtures: `boardtest.Load`, a `ResetMail` that returns the token in the body, and `allow` that returns true unless a case says otherwise)
  - `TestRequestResetByNameOrEmail`: alice by `Alice`, by ` alice ` and by her address in upper case each queue 1 email to her address. The `password_resets` row holds the SHA-256 of the token in the body, with `expires_at` = now + 1 h. The queue row has `send_by` equal to it.
  - `TestRequestResetQuiet`: each of these queues nothing, makes no link and returns `0, nil`: an unknown name; an inactive member; a member with a user ban; a member whose address an email ban holds; a member with an empty address; a member without `account.password`; `allow` false; a name of 513 bytes; a name with a NUL.
  - `TestRequestResetIgnoresAddressBan`: a ban on the client's address changes nothing: alice gets her email.
  - `TestRequestResetSharedAddress`: two accounts share one address. A request by the address queues 2 emails, each with its own username and token. `CompleteReset` with the first token changes only the first account.
  - `TestResetLinkRules`: a live link gives alice's ID and username. Each of these gives `ErrNotFound`: 1 h and 1 s later; a token of 42 characters; a link after a change of the password on the account page; after `ResetPassword` by the administrator; after a user ban.
  - `TestCompleteReset`: the new password verifies with `password.Verify`; `password_scheme` is `gobb`; the epoch is one more; every session of alice is gone; `email_verified` is true; alice has no link left; one `mod_log` row as above.
  - `TestCompleteResetSingleUse`: a second call with the same token gives `ErrNotFound`, and the password stays the first one.
  - `TestCompleteResetRefusesWeak`: `short1234` gives the field `next` and `Use at least 10 characters.`; the username gives `Choose a password that differs from your username.`. The link is still live after each.
  - `TestCompleteResetRace`: a `hold` that changes alice's password on the account page, then returns. `CompleteReset` gives `ErrNotFound`, and the account page's password stands.
  - `TestEmailChangeEndsResetLinks`: after `ChangeEmailWith` to a new address, the old link gives `ErrNotFound`. After a change to the same address, the link stays live.
  - `TestCleanupResets`: an expired link goes; a live one stays.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/board/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Make the token as `session` makes a session token. Add `board.CleanupResets` to the hourly `cleanup`.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "board: password reset links, single-use, one hour"`

**Checkpoint:** none.

---

### Task 4: The reset pages

**Files:**
- Create: `internal/web/reset.go`, `internal/web/reset_test.go`, `internal/web/templates/reset.html`, `internal/web/templates/reset-link.html`
- Modify: `internal/web/site.go` (`Options`, the routes, `pageFiles`, the limiters), `internal/web/headers.go` (`noindexPages`), `internal/web/auth.go` and `internal/web/templates/login.html` (the link and the notice), `internal/web/static/gobb.css` if a class is new, `cmd/gobb/main.go` (pass `Mail`), `README.md` (the reset section), `docs/changelog.md`

**Interfaces:**
- Consumes: `board.RequestReset`, `board.ResetLink`, `board.CompleteReset`, `board.ResetMail` (Task 3); the `wake` channel of `serve` (Task 2); `s.login.Hold` (existing).
- Produces:
  - `web.MailOptions{PublicURL string; Wake func()}`. `Wake` must not block.
  - `web.Options.Mail *web.MailOptions`: nil turns the reset pages off.
  - `web.Options.ResetFloor time.Duration`: zero means 400 ms.
  - Four routes, as "URLs" says, registered only when `Mail` is not nil. Both POSTs use `s.protect(maxLoginBody, …)`.

Rules:
- The link in an email is built from `MailOptions.PublicURL` and the token, never from `r.Host`, a forwarded header or the base path of the request.
- `POST /reset` checks the address limit, then the board limit, then calls `RequestReset`. Its `allow` takes from the per-account limiter. After a queue of one or more, it calls `Wake`. It waits until `ResetFloor` after its start, then answers 303. A refused CSRF token or an empty name answers before the limits count.
- `GET /reset/{token}` and `POST /reset/{token}` answer a dead link with the 404 page, and count a dead-link hit.
- `POST /reset/{token}` checks `again` before any hash. `CompleteReset` runs with `hold` = `s.login.Hold`. A full gate gives 503 with `Retry-After: 5`. Success clears the session cookie of the visitor when its session ended, and answers 303 to `/login?reset=1`.

- [ ] **Step 1: Write the failing tests** (a site with `Mail` set, `ResetFloor` of 50 ms, and a `Wake` that counts)
  - `TestResetRoutesOffWithoutMail`: with `Mail` nil, `GET /reset` and `GET /reset/x` answer 404, and the sign-in page holds no `Forgot your password?`.
  - `TestResetSignInLink`: with `Mail` set, the sign-in page links to `{base}reset`. `/login?reset=1` shows the notice.
  - `TestResetRequestSameAnswer` (Review Focus 4): POSTs for alice, for an unknown name, an inactive account, a banned account, an account without `account.password`, and alice again after her limit is spent. Each answers 303 to `/reset?sent=1`, with the same `Set-Cookie` names, after at least 50 ms. The queue holds rows for alice's first request only. `Wake` ran once.
  - `TestResetLinkIgnoresHost` (Review Focus 1): a POST for alice with `Host: evil.example`, from a trusted proxy with `X-Forwarded-Host: evil.example`. The queued body holds `https://board.example.org/forums/reset/` and no `evil`.
  - `TestResetGetDoesNotConsume` (Review Focus 2): three GETs of alice's link answer 200, and a HEAD answers 200 with no body. A POST of a good new password then answers 303. A second POST answers 404.
  - `TestResetLinkPage`: the answer has `Referrer-Policy: no-referrer`, `Cache-Control: no-store` and `noindex`. The page holds alice's name in `<bdi>`, the hidden `username` field, and no `http` URL other than the site's.
  - `TestResetFlow`: alice signs in (session A). A request queues her link. A POST sets `NewPassword2026`, and the answer is 303 to `/login?reset=1`. Session A is dead. A sign-in with the old password fails, and one with `NewPassword2026` succeeds.
  - `TestResetPasswordsDiffer` and `TestResetWeakPassword`: 400 with the message beside the field, no hash run (for the first), and the link still live.
  - `TestResetLimits`: the 6th POST from one address in an hour answers 429 with `Retry-After`. The 31st POST of the board in an hour, sent from 7 addresses through a trusted proxy, answers 429. The 4th request for alice in a day answers 303 and queues nothing. The 21st dead-link hit from one address answers 429.
  - `TestResetCSRF`: `POST /reset` without the token answers 403. A cross-origin POST answers 403. Neither counts against a limit.
  - `TestResetLogsNoSecret`: capture slog through `TestResetFlow`'s steps. No line holds the token, `reset/`, alice's address or the new password.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement** the pages, the limits and the wiring in `serve` (`Mail` from `cfg.Mail`, `Wake` as a non-blocking send on the `wake` channel). In the README, replace "Password reset by email is not built yet" with the member's way (the link on the sign-in page), and keep `gobb user reset-password` as the administrator's way.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: forgot-password pages, with limits and no account disclosure"`

**Checkpoint:** the controller starts the private copy with `mailtest`'s settings pointing at a local catcher, asks for a reset for its own test account, opens the link and sets a password. No real address gets mail.

---

### Security review (controller)

An Opus reviewer reads Tasks 1–4 against the Global Constraints and the Review Focus, as an attacker would: link poisoning, account disclosure through answers or timing, token leaks (logs, Referer, the queue, errors), TLS downgrade, header injection, mail bombing, races between a reset and a password change, and a request that waits on SMTP. One fix dispatch, then one scoped re-review.

---

### Task 5: Release 0.3.0 and the rehearsal

**Files:**
- Modify: `docs/changelog.md` (date the `0.3.0` section), `internal/version/version.go`, `README.md` (the status and the roadmap row)

- [ ] **Step 1: Bump the version.** Set `internal/version` to `0.3.0`, and date the changelog section. Run `go run ./scripts/releasecheck -dry-run v0.3.0`. Expected: it passes.
- [ ] **Step 2: Commit and push.** `git commit -m "release: 0.3.0"`
- [ ] **Step 3: Tag `v0.3.0`, with the owner's go-ahead at the time.** Watch the release workflow until the image smoke-runs and prints `0.3.0`.

**Checkpoint (controller and owner):** this needs the sending account. The rehearsal runs on the production host from the owner's private notes, with each step on the owner's go-ahead:
- the SMTP settings go into the host's `.env`, and the owner types the password;
- `gobb mail test` reaches the owner's own address;
- a fresh bundle and the import into the 0.3.0 image;
- the owner asks for a reset on a real account, gets the email, opens the link, sets a password and signs in with it;
- the same link fails a second time.

---

### Final review (controller)

Once Task 5 is green, the Opus reviewer reviews the whole plan's work against the spec sections in the header and the Review Focus. Then one fix dispatch, and one scoped re-review.

## Order

The tasks run in order, one at a time.
- Task 2 needs Task 1's message checks.
- Task 3 needs Task 2's migration and `mail.Enqueue`.
- Task 4 needs Task 3, and Task 2's `wake` channel.
- The security review comes after Task 4, and Task 5 after the review's fixes.
- The private how-to gains the SMTP settings and the reset check before Task 5's rehearsal.
