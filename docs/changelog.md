# Changelog

## 0.8.0 (2026-10-27)

The admin panel (evanwtf/gobb#18): web pages for the work that needed the command line
or a hand-written SQL statement. It comes in parts; each part below is one pull
request. The date is the day of the tag.

### The admin area (evanwtf/gobb#19)

- **`/admin/`**, for members with the board-wide `admin` right. Anyone else, a guest
  too, gets the 404 page of any missing path, on GET and POST, so the area does not
  show that it exists. Every admin route sits on one mux behind that gate; a test
  walks the routes of the running site, so a new route cannot skip it.
- **The Admin link** in the nav, for admins only.
- **The log page,** `/admin/log`: `mod_log`, newest first, 50 rows a page, with the
  actor, the action, the target (linked, with its name) and the details. A row with no
  actor shows "Command line" or "Reset link". Filters: action, member, target type
  and ID, and "only actions with no member".
- **Confirm your password,** `/admin/confirm`: the dangerous changes of the later
  parts ask for it. The check is the one of the account page, and it shares the limit
  of the password form there (5 tries an hour). A pass lasts 10 minutes on the session,
  in memory. A dangerous write without a pass goes to the confirm page and does not
  run.
- **A limit on admin writes:** 120 POSTs per admin in 10 minutes, counted in
  `gobb_rate_limited_total{limit="admin_writes"}`.
- Every admin page carries `noindex` and `Cache-Control: no-store`.
- **One write helper,** `board.AdminWrite`: the change and its `mod_log` row, with the
  admin as the actor and `{"via":"web"}`, in one transaction.

## 0.7.0 (2026-10-27)

Private messages read as conversations (evanwtf/gobb#32). A reply joins the
conversation of the message it answers, and one page shows the whole talk, both sides,
oldest first, with a reply form at the bottom. Before, a reply was a new message in
the inbox, and the member's own replies were on the Sent page.

Operators: migration 0015 adds `pms.conversation_id`. Every existing message starts
its own conversation; run `gobb messages thread` once to join the old replies (below).
The bundle format does not change.

### Members

- **One list of conversations.** `/messages` lists conversations, the one with the
  newest message first. A row shows the subject, the other people, the count of
  messages and unread ones, and the time and author of the last message. Sent stays,
  as a second tab, for the messages the member wrote.
- **The conversation page.** `/messages/{id}` shows the conversation of the message,
  at the page that holds it (25 messages a page). Each message has its own To line,
  Quote and Delete. The reply form answers the last message; "Change the people or the
  subject" opens the full form. Opening the page marks the messages on it read.
- **What a member sees.** Only the messages the member is a party to, as before: a
  blind-copy recipient sees the messages that went to them, and a deleted message is
  gone from the member's view. Delete still removes one message. A new reply in a
  conversation the member deleted shows again, with the new message only.
- **The edit note** reads "Edited by NAME, TIME." It said "on 4 minutes ago" in the
  relative date format (evanwtf/gobb#33).

### Operators

- **`gobb messages thread [-dry-run]`** joins old replies. In ID order, a message whose
  subject starts with "Re:" (in any case, any number of times) joins the conversation
  of the newest earlier message with the same subject without the prefixes, in any
  case, and the same people: the author and the "to" recipients, as a set. A message
  without "Re:" starts a conversation. A message that has joined one stays in it, so a
  second run joins nothing. `-dry-run` counts and changes nothing.
- **A reply's link.** The reply forms send `reply_to`, the ID of the message answered.
  When the writer is not a party to it (a message of someone else, one the writer
  deleted, or no such ID), the message starts its own conversation. The answer is the
  same in each case, so the field tells nothing about other messages.
- **The send redirect** goes to `/messages/{id}#m{id}`, and a delete to `/messages`.

## 0.6.0 (2026-10-26)

The metrics show how full each rate limit gets, not only how often it refuses
(evanwtf/gobb#29). An owner can now tell whether a limit is needed (how close real keys
come to it) and whether it works (what share of checks it refuses, how many keys sit at
the limit, whether the key table fills and pushes keys out).

Operators: no migration and no new setting. The new series appear on the metrics
listener only. The bundle format does not change.

### Operators

- **New series, per limit.** Every one has the label `limit`, one of the 14 names, and
  never a key. `gobb_rate_limit_allowed_total` counts the events that a limit recorded
  (an allowed `Take` or `TryTake`, and each `Add`). `gobb_rate_limit_fill_ratio` is a
  histogram with the buckets 0.25, 0.5, 0.75, 0.9 and 1; each recorded event adds one
  observation: the events of its key in the window after it, over the limit.
  `gobb_rate_limit_refunds_total` counts the events that `Refund` removed.
  `gobb_rate_limit_evictions_total` counts the keys with events in the window that a
  full table dropped to make room. Keys with no event in the window are dropped as
  before and are not counted, because they lose nothing.
  `gobb_rate_limited_total` is unchanged.
- **What a refund does.** A refund lowers neither `gobb_rate_limit_allowed_total` nor
  the histogram, and `gobb_rate_limit_refunds_total` counts it. The net events recorded
  are allowed minus refunds. The histogram is the fill at the time of the check,
  refunded or not. Sign-in takes its event before the password check and gives it back
  after a good password, so `allowed_total` for `signin_address` counts every attempt
  that the limit let in.
- **What the refusal share leaves out.** For `signin_name`, a check that finds the name
  full and then finds that the account has used the address lets the request pass. That
  check counts as neither allowed nor refused (it is no refusal, as in 0.5.0), so
  refused over allowed plus refused leaves it out.
- **Gauges read at each scrape.** `gobb_rate_limit_keys` (keys held, live or not),
  `gobb_rate_limit_keys_full` (keys with as many events in the window as the limit) and
  `gobb_rate_limit_fullest_ratio` (the highest fill of any key). The settings
  `gobb_rate_limit_max_events`, `gobb_rate_limit_window_seconds` and
  `gobb_rate_limit_max_keys` say what each limit is. The scan reads the keys under the
  lock of the limit and changes nothing. It is bounded by `maxKeys`: 0.14 ms at 10,000
  keys and 1.5 ms at 100,000 (`BenchmarkScan`, Apple M5 Max). A scrape scans each
  limit once, at one time, and writes all six gauges from that scan, so they agree
  (`metrics.Registry.GaugeGroup`).
- **Dashboards.** The share of refusals is refused over allowed plus refused. How full
  keys get is the p50, p90 and p99 of the fill ratio. A key table under pressure shows
  as evictions above 0.
- **Named limits.** A limit built twice under one name (a test builds the server more
  than once) replaces the first in the gauges. A limit with no name records nothing.
  The README "Metrics" section lists the new names.
- **Tests.** `TestRateLimitMetricsSequence` pins every new metric to an exact value
  after a known run of takes, refusals, refunds and evictions;
  `TestRateLimitEvictionsCountLiveKeysOnly`,
  `TestRateLimitMetricsRecordOnlyRecordedEvents`, `TestRateLimitNamedTwiceReplaces`,
  `TestRateLimitRenamed`, `TestRateLimitMetricsParallel`, `TestScan`, `TestScanEmpty`,
  `TestScrapeGaugesAgree`, `TestGaugeSamples`, `TestGaugeGroup` and the new lines in
  `TestMetricsEndpoint`.

## 0.5.0 (2026-10-26)

gobb serves Prometheus metrics on a second listener, so an operator can see requests,
errors, sign-ins, rate limits and the email queue from outside the log
(evanwtf/gobb#14). The listener is off until `GOBB_METRICS_LISTEN` is set.

Operators: no migration. With `GOBB_METRICS_LISTEN` unset, 0.5.0 serves as 0.4.0 did.
The bundle format does not change.

### Operators

- **The metrics listener.** `GOBB_METRICS_LISTEN` (for example `127.0.0.1:9187`)
  starts a second HTTP listener that serves only `GET` and `HEAD /metrics`; every other
  path is 404 and every other method 405. It is outside the base path and the main
  listener. The endpoint has no auth, so an address that is not loopback or private
  logs a WARN line at start. Every interface is one of those; inside a container it is
  the usual address, and the line says to publish the port on loopback only. The value must be `host:port` with a port from 1 to 65535, and not
  the address of `GOBB_LISTEN`. The listener stops at shutdown before the pool closes.
  `compose.yaml` passes the variable through and publishes no new port. The new
  `compose.metrics.yaml`, added to `COMPOSE_FILE`, turns the listener on and publishes
  container port 9187 on the host's `127.0.0.1` at `GOBB_METRICS_PORT`. The README has a
  "Metrics" section.
- **No client library.** `internal/metrics` writes the text format, version 0.0.4:
  counters, gauges, histograms and values read at scrape time, sorted by name and by
  label values, with escaped label values and help text. A whole number prints as an
  integer. CI runs `promtool check metrics` (Prometheus 3.15.1) on the scrape of
  `TestMetricsEndpoint`, and the image job scrapes `/metrics` from the running
  container.
- **What is counted.** Requests and their durations by route pattern (never the path),
  method and status class; sign-in checks by result; each request that a named rate
  limit refuses (`ratelimit.Limiter.Named`; all 14 limits have names; `Take` counts its
  refusals, `Over` only reads, and sign-in counts the name limit itself, because a
  locked name still lets the member in from a known address); emails sent,
  failed and dropped; reset emails skipped by reason (the board's hour and day, the
  account's day, an administrator, an ineligible account). Gauges: the email queue and
  the live sessions (one query each, at most every 15 s), the sign-in floor, the
  memory gate (`login.Gate.Stats`), the database pool, the build, the Go runtime and the
  process. The gate metrics replace the `gobb_password_checks_in_flight` of #14: the
  gate also holds avatar decodes, so `gobb_memory_gate_holders` says what it counts. A
  request whose handler panics counts with the status it sent, or as a 500.
- **Tests.** `TestMetricsEndpoint`, `TestStartMetricsOff`, `TestLogMetricsAddr`,
  `TestFromEnvMetricsListen`, `TestWriteFormat`, `TestHandler`, `TestCached`,
  `TestMeteredCountsByPattern`, `TestMeteredEdges`, `TestCheckCountsResults`,
  `TestCheckCountsLimited`, `TestKnownAddressIsNoRefusal`, `TestSigninResult`,
  `TestNamedCountsRefusals`, `TestGateStats`, `TestProcessCountsOutcomes`,
  `TestRequestResetCountsSkips`, `TestHistogramScrapeDuringObserve`,
  `TestRegisterOtherTypePanics`, and the skip counts in `TestResetLimits`.

## 0.4.0 (2026-10-26)

Administrators activate and deactivate accounts from the command line. An inactive
member cannot sign in and gets no reset email. Until now, an account that arrived
inactive from an imported board stayed inactive unless someone changed the database.

Operators: no migration and no new setting. The bundle format does not change.

### Operators

- **`gobb user activate <name>` and `gobb user deactivate <name>`.** The name matches as
  for the other `gobb user` commands. `activate` sets the account active; the password,
  the email address and the bans stay, so a banned member stays banned. `deactivate`
  sets the account inactive and ends every session of the member; a reset link sent
  earlier stops working, because the link page reads only active accounts. The posts,
  the password and the address stay, so `activate` gives the same sign-in back. Each
  change writes one `mod_log` row (`user.activate`, `user.deactivate`) with a null actor
  and `{"via":"cli"}`. An account that is in the state already exits 0, says so on
  stderr and writes nothing. `board.ActivateUser` and `board.DeactivateUser` take
  `for update` on the user row first, as the reset and the ban do. A sign-in in flight
  holds that row `for share` and reads the state in `session.CreateChecked`, so it
  either finishes first and loses its session to the deactivate, or waits and is
  refused. Tests: `TestActivateUser`, `TestDeactivateUser`, `TestDeactivateAndActivate`;
  the deactivate joins the two lock-order tests, now named
  `TestCLIWritesWaitForASignInAndEndItsSession` and
  `TestCreateCheckedWaitsForTheRowLockOfACLIWrite`.

## 0.3.0 (2026-10-26)

Members who forgot their password reset it themselves. The sign-in page links to a form
that takes a username or an email address, and the board emails a link that works once
and for 1 hour. The form gives the same answer whether or not the name has an account.
Administrators get no reset email and reset with `gobb user reset-password`.

Operators: a 0.2.0 database migrates in place (migration 0014) when the server starts.
Email is off until `GOBB_SMTP_HOST` is set; with it off, 0.3.0 serves as 0.2.0 did, and
the reset pages answer 404. The bundle format does not change.

### Operators

- **Email settings and the SMTP sender.** Seven variables turn email on:
  `GOBB_SMTP_HOST`, `GOBB_SMTP_PORT`, `GOBB_SMTP_TLS` (`starttls` or `implicit`; there is
  no plain mode), `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD`, `GOBB_MAIL_FROM` and
  `GOBB_PUBLIC_URL`. With no host, email is off and any other of the seven is an error,
  as is `GOBB_MAIL_PER_HOUR` or `GOBB_MAIL_PER_DAY` (the limits on reset emails, below).
  The password prints as `[set]` or `[unset]` in `String()`, `%+v` and slog, and no error
  holds it. `internal/mail` builds the message (plain text, UTF-8, quoted-printable, CRLF)
  and sends it over TLS only, with AUTH PLAIN after TLS. It sends nothing but QUIT to a
  server that does not offer STARTTLS, and it gives up after 30 s. An error holds the
  stage and a summary (`smtp 550 5.1.1`, `timeout`, `tls`, `no starttls` or `network`),
  not the text of the server's reply, the password or the address.
  `gobb mail test <address>` sends one test email at once and needs no `GOBB_DSN`.
  `compose.yaml` passes the seven variables and the two limits through, and the README
  has an "Email" section.
- **The email queue.** Migration 0014 adds the table `password_resets` and three columns
  to `email_queue` (`user_id`, `send_by`, `dropped_at`). `mail.Enqueue` writes a message
  to the queue inside the caller's transaction and refuses a bad address first. With
  email on, `gobb serve` runs one worker. It wakes every 30 s, claims up to 10 due rows
  with a 5-minute lease and sends each row with no database connection open. A failed
  send waits 1, 5, 15 and then 30 minutes. A 5xx reply, a passed `send_by` or the 8th
  failed attempt drops the row and clears its body. A stop of the server during a send
  does not count as a failure. `last_error` holds `smtp 550 5.1.1`, `timeout` or
  another short summary, and the log holds the row ID and the member ID, never the
  address, the subject or the body. The hourly cleanup deletes rows 30 days after they
  were sent or dropped.
- **Password reset in the board.** `board.RequestReset` looks a name up as sign-in does (a
  username, then an email address, at most 5 accounts) and queues one email for each
  active account that has a valid address, has `account.password`, does not have the
  board-wide `admin` right (an administrator uses `gobb user reset-password`) and is under
  no user ban and no email ban. Everything else does nothing, and the answer is the same.
  A link is 32 random bytes in base64url, lives 1 hour, and the database holds only its
  SHA-256. `board.CompleteReset` checks the new password with the account page's rules,
  then in one transaction stores a gobb hash, adds one to the password epoch, sets
  `email_verified`, deletes every session and every link of the member, and writes one
  `user.reset_password` row (`{"via":"email"}`) to the moderation log. A link works once.
  It dies when the password epoch moves (a reset, an account page change, an
  administrator's reset), when the member changes the email address and when a user ban
  lands, and when the member gets the `admin` right. No token, link, address or password
  is in a log line, an error or a log row. The hourly cleanup logs the email rows and the
  expired reset links as two counts.
- **Forgot-password pages.** With email on, the sign-in page links to `/reset`. The form
  takes a username or an email address and always answers `303` to `/reset?sent=1`, after
  at least 400 ms, whatever the name finds. A link opens `/reset/{token}`: a GET or HEAD
  changes nothing, and the POST that saves the new password (typed twice) uses the link
  up, ends every session of the member and sends the member to sign in with a notice. A
  dead link gets a 404 page. The link in the email is built from `GOBB_PUBLIC_URL` and
  nothing in the request. Limits, in memory: 5 requests an hour from one address, 20
  dead-link hits an hour from one address, 3 emails a day to one account, and on the whole
  board `GOBB_MAIL_PER_HOUR` emails an hour and `GOBB_MAIL_PER_DAY` a day (10 and 20 by
  default; from 1 to 100,000, and the hourly limit not more than the daily one). A
  request or a dead-link hit over the limit of its address answers `429` with
  `Retry-After`. An account over its limit, or a request that the limit
  of the board stops, gets the usual answer and no email: a `429` that shows only for a
  real account would tell a visitor which names exist. Requests for unknown names do not
  count against the limit of the board. A completed reset clears the session cookie of the
  visitor only when the visitor is the member whose password changed. The pages carry
  `noindex` and `no-store`, and the link page also carries `Referrer-Policy: no-referrer`.
  A board name with a control character is folded to spaces in the subject. With email
  off, the four routes answer 404 and the sign-in page has no link. A request never waits
  on SMTP: it queues the email and wakes the worker.

## 0.2.0 (2026-10-25)

Members change their own settings and send private messages. The account page saves each
setting as it changes: a time zone (a place, with its daylight saving time), a date format
(standard, relative or ISO), an avatar upload, "About you" and a birthday. Profiles show
"About you" and the age, and the board index lists today's birthdays. Private messages
have an inbox and a sent list, and members send, reply, quote and delete. The header shows
the count of unread messages.

Operators: a 0.1.0 database migrates in place (migrations 0012 and 0013) when the server
starts. Bundle format version 2 is required, and gobb refuses a bundle of version 1, so a
converter must move to version 2. Topic watches and bookmarks that a 0.1.0 import loaded
stay in the database, and gobb has no page for them.

### Members

- **Account page.** It gains a time zone list ("Common" first, then every zone), a date
  format choice with a sample of each format, "About you" (location, occupation,
  interests, website, and the birthday's day, month and year), and an avatar with "Remove
  avatar". The signature saves the same way. `gobb.js` sends a field when its `change`
  event fires, with `X-Gobb-Save: 1`:
  - one save at a time for each field: a change made meanwhile waits, and only its latest
    value goes;
  - a list waits 400 ms after its last `change` before the save starts, so the arrow keys
    on a long list send one save; a radio button, a check box and a text box save at once;
  - a pending change goes out as the page closes (`keepalive`);
  - the status line says "Saving…", then "Saved." or the message of the server.

  A save answers 204, or JSON (`{"field","message"}` with 400, `{"message"}` for 403, 404,
  413, 429 and 503). A refusal of the CSRF or cross-origin check, a body over its cap and
  a form that cannot be read answer in JSON too. An avatar upload answers 200 with
  `{"src"}`. Without script, each section keeps its Save button: the page shows "Save each
  section with its button." in place of "Changes save as you make them.", the buttons post
  the forms, and the answer is a 303 to `/account?done=...`, or the page again with the
  message beside the field and every value that was sent. The routes are
  `/account/timezone`, `/account/dateformat`, `/account/about`, `/account/avatar` (a
  multipart upload of at most 1 MiB) and `/account/avatar/remove`. `POST /account/about`
  changes only the fields it holds, and the birthday only when day, month and year are all
  present. The text fields and the birthday are two writes, so a refusal of one never
  drops what was sent for the other. A field of white space only counts as not set.
- **Avatar.** The server turns an upload into a new 128 x 128 PNG named
  `avatars/u{id}-{16 hex}.png`, and deletes the old file after the database write commits.
  It keeps the old file when another member names it (a bundle lets several rows share one
  file) or when gobb did not make the path. It reads at most 1 MiB, takes the format from
  the first bytes (PNG, JPEG or GIF; WebP is refused), and refuses more than 8,000 pixels
  a side or 16,000,000 in all. It decodes the first frame, crops the center square, and
  resamples it. Only pixels reach the output, so metadata and trailing bytes do not
  survive.
- **Times.** A member picks `standard` (`Oct 25, 2026, 5:07 pm`), `relative` (`3 hours
  ago`, with the standard text in the `title`) or `iso` (`2026-10-25T17:07:00-04:00`); a
  guest sees `standard`. Ages under 180 days count elapsed time, so a change of daylight
  saving time does not shift them; older ages count whole calendar months and years in the
  viewer's zone. A relative time more than a minute ahead keeps the layout of its call (a
  date stays a date). "Posts since" on the new-posts page shows the standard time for a
  member whose format is relative. The `<time>` element keeps `datetime` in UTC.
- **Profiles.** A profile shows the age, the location, the occupation, the interests and
  the website of the member, each only when set, after "Last visit". The age counts whole
  years on the calendar date of the viewer's zone. A member born on 29 February has the
  birthday on 28 February in a year that is not a leap year, for the age and for the list
  alike. The website link shows the host and the path without the scheme, and carries
  `rel="nofollow ugc noopener"`. The text drops the query, the fragment and Unicode format
  characters, and the link keeps the address as stored. A profile shows "Send a message"
  to a viewer who may message the member.
- **Birthdays.** The board index lists "Birthdays today" at its end, for a viewer with
  `account.view_profiles`: active members whose day and month are today in the viewer's
  zone, with the age when the member gave a year.
- **Header.** The member's links sit in the header, at the right of the board name:
  "Signed in as {name}" and Sign out, or a guest's Sign in link. The name links to
  `/account` and carries `aria-current="page"` there. A long name ends in an ellipsis (the
  full name is the link's `title`). The links hide in print.
- **Private messages.** The pages are `/messages` (inbox), `/messages/sent`,
  `/messages/{id}`, `/messages/new` (with `?to=` to fill the To field),
  `/messages/{id}/reply` (with `?quote=1` to quote), and `POST /messages/{id}/delete`,
  which asks first, as a post does. A guest goes to `/login` and comes back; a guest POST
  gets 403.
  - A send takes 1 to 10 members, a subject of 1 to 120 characters and a body of up to
    60,000 bytes. More than one member needs `pm.send_many`; without it, the compose form
    says "You can send a message to one member at a time." The To field takes names split
    on commas, and one name that holds a comma wins over a list.
  - The text goes through `ParseMessage`, which uses the one parser with `pm.markdown`,
    `pm.smilies` and `pm.images`. Embeds in a message and in its preview show as links.
  - A message that the author sent twice within 60 seconds is stored once. A reply to a
    message of your own goes to its recipients (not its blind-copy recipients).
  - A message page shows the subject, From, To, Bcc (to the author, and to a blind-copy
    recipient for the own name), the time, the body, the signature, and Reply, Quote and
    Delete each only with its right. The sent list shows To only.
  - A message that the viewer did not write or receive gives 404, the same as a missing
    one, and a blind-copy recipient shows only to the author and to that recipient. A
    delete hides the message from the viewer's own view and removes no row.
  - The nav shows "Messages" after Team for a member with `pm.read`, with a `badge-new`
    pill "N new" when unread messages wait. A count that fails logs a WARN line and shows
    no pill.
  - A member sends at most 10 messages in 10 minutes. A refused send does not count, and a
    preview or a send also counts against the posting limit. The compose form keeps every
    value on a refusal (400) and on a limit (429).

### Security

- **No third-party scripts.** X, Instagram and Threads links show as plain links in every
  place: posts, previews, signatures, messages and static pages. So no page loads a script
  from another site. YouTube, Vimeo, TikTok, Spotify and SoundCloud stay embedded in
  frames. Stored posts do not change, because the choice is made when a page renders.
- **Hidden formatting characters.** "About you", the website, topic titles, post subjects
  and message subjects refuse Unicode format characters (category Cf) and the line and
  paragraph separators, with three exceptions: the zero width joiner (U+200D), the zero
  width non-joiner (U+200C) and the tag characters U+E0020 to U+E007F. So "👨‍💻 Dev", a
  subdivision flag and Persian text pass, and U+202E, U+200B and U+FEFF do not. The
  website check also decodes the path, the query and the fragment, and refuses a bare
  `%`. The bundle check applies the same rule and says "holds a hidden formatting
  character"; the "About you" field says "Remove the hidden formatting character." A
  title or subject with U+2028 or U+2029 gets the message of a control character or line
  break. Pages isolate every title and subject in `<bdi>`, so a bidirectional control
  cannot reorder the text around it.
- **Views, visits and read marks.** A topic view and a visit count on any top-level
  navigation (`Sec-Fetch-Dest: document`, or `Accept: text/html` when the browser sends no
  fetch metadata) that is not a prefetch, whoever started it. A message or a topic is
  marked read only when the member started the navigation: the browser sends no
  `Sec-Fetch-Site`, or sends `same-origin` (a click on this site) or `none` (a typed
  address or a bookmark). A link on another site opens the page and counts the view and
  the visit, but sets no mark. `board.PeekMessage` reads with no write, and
  `board.MarkMessageRead` marks. An image that points at a topic or message address
  changes nothing.
- **Pages behind sign-in.** The account and message pages carry `noindex` and
  `Cache-Control: private, no-store`. The redirect of a guest from `/account` carries
  `noindex` and `private, no-cache`.
- **Uploads.** An upload needs a signed-in member with `account.avatar` before the server
  takes a slot or reads the body (403 otherwise). One member has one upload in flight (a
  second gets 503), and the server takes four uploads at a time (the fifth gets 503).
- **Limits.** A member saves 120 times in 10 minutes (the signature form counts too, with
  or without script) and uploads 10 avatars in an hour.
- **Memory.** `avatar.Make` reads the size from the header and charges the memory of the
  decode to the sign-in gate by format before it decodes: JPEG 19 bytes a pixel for three
  components (a late Adobe segment turns YCbCr into RGB after the header), 24 or 5 by
  color model otherwise, on sides padded to 32; PNG 4, or 8 for 16 bits, doubled when
  interlaced; GIF 4; plus 1 MiB. A decode that is more than the whole budget
  (`Gate.Budget`) is refused with `avatar.ErrTooMuchMemory` and the text "That image
  needs too much memory to process. Use a smaller one.", so none runs alone and breaks
  the memory limit of the container. The share goes back through
  `Gate.AcquireCollected`, which runs a garbage collection first. The sign-in uses it
  too.
- **Refusals.** A member who went inactive during a refused upload or a 429 page gets 404
  with the neutral text. The 429 pages of the time zone, the date format, "About you" and
  the signature show the values that were sent.

### Operators

- Migration `0012_settings_messages.sql` adds the member settings to `users`:
  `date_format` (`standard`, `relative` or `iso`), `location`, `occupation`, `interests`,
  `website`, and the birthday as `birth_day`, `birth_month` and `birth_year`. Checks hold
  the length limits and the birthday pairing. It adds three indexes for the birthday list,
  the inbox and the sent list. `gobb serve` applies it at start, and `gobb migrate`
  applies it alone.
- Migration `0013_legacy_message_routes.sql` widens the check on
  `legacy_routes.target_kind` to the kinds `messages` and `message`, for the old links to
  private messages that bundle format 2 carries. A database that has applied migration
  0001 keeps its record. `gobb serve` applies 0013 at start.
- The importer writes the new columns. It no longer loads `topic_watches.jsonl` or
  `bookmarks.jsonl` and no longer writes `notify_on_post`. The tables `topic_watches` and
  `bookmarks` stay in the schema, and they stay empty after an import.
- `gobb.css` gains the styles of the settings forms: `fieldset.field` and its `legend`,
  `.save-status`, `.save-fallback`, `.birthday-row` and `.avatar-img`. The checkbox
  labels of the account page are regular weight, and a guest's Sign in link has the
  padding and the hover of Sign out.
- CI runs `FuzzMake`, the fuzz target of the avatar maker, in the fuzz job.
- For code that calls the board package: `SetTimezone`, `SetDateFormat`, `SetAbout`,
  `SetBirthday`, `SetAvatar` and `RemoveAvatar` write the settings, and `Inbox`, `Sent`,
  `PeekMessage`, `MarkMessageRead`, `UnreadMessages`, `SendMessage`, `DeleteMessage` and
  `ReplyDraft` read and write messages. `board.ValidAvatarPath` is the rule for a stored
  path that gobb may delete. `board.Profile` takes the current time, and
  `ProfilePage.CanMessage` tells whether the viewer may send the member a message. Every
  page builds a `clock` from the viewer and the request time, and the `time` and `date`
  template functions take it in place of a zone. A nav link can carry a `badge-new` pill
  (`navLink.Badge`).

### Bundle

- Format version 2 (`docs/bundle.md`). `users.jsonl` drops `notify_on_post` and adds
  `date_format`, `location`, `occupation`, `interests`, `website`, `birth_day`,
  `birth_month` and `birth_year`. The format drops `topic_watches.jsonl` and
  `bookmarks.jsonl`, so it has 21 data files. A bundle of version 1 is refused with a
  message that tells the converter author to build it again.
- `settings.timezone` and `users.timezone` must be places from the IANA `zone.tab` file
  (`internal/tz`, tzdata 2026c, generated by `scripts/zonetab`): a fixed offset, `UTC` and
  an alias are refused. `bundle.KnownZone` lets a converter outside this module map its
  zones.
- `legacy_routes.jsonl` gains the kinds `messages` (to `/messages`) and `message` (with an
  ID, to `/messages/{id}`) for old message links. The redirect is 301, as for the other
  kinds. A guest signs in and comes back, and a member who is not a party to the message
  gets 404. `bundle.Check` refuses a `message` route with no ID or with an unknown
  message.
- The rules for the website, the profile text and the birthday live once, in
  `internal/account/profile.go`, for the bundle check and for the settings pages. The
  bundle check takes the latest birth year from `account.CheckBirthday`.

## 0.1.0 (2026-10-25)

The first release. gobb is one Go server for a forum, backed by PostgreSQL 16 or later
(the Compose stack runs PostgreSQL 18). It can take over an existing board from an
import bundle, with every member's password intact.

- Members read the board, search it, post new topics, reply, quote, preview, and edit
  or delete their own posts. They change their password, email and signature on the
  account page.
- Moderators delete and restore posts, edit them with a reason, lock and unlock topics,
  and approve held posts, on the page itself. Admins reset passwords and ban or unban
  members from the command line.
- Every piece of member text goes through one parser and one renderer. Pages run under
  a strict Content Security Policy. CI runs a corpus of script-injection payloads,
  fuzzes the parser, and checks that the parse time of each known hostile shape grows
  in step with the size of the post.
- The release ships a container image for amd64 and arm64, and binaries for Linux and
  macOS. The README's backup, upgrade and restore steps run in CI on every push.

Later releases add private messages, polls, attachment uploads, topic watches, email,
password reset by email, open sign-up and the admin panel.

The entries below follow the work plan by plan.

Plan 1, the foundation: the pieces that the pages stand on.

- Repo guards: `scripts/check-public.sh` refuses private addresses (LAN and
  carrier-grade NAT ranges), home-directory paths, private and tailnet host names, and
  the words of the owner's private denylist. `scripts/refuse-session-ids.sh` refuses
  agent session details in commit messages. Both run in pre-commit and in CI, and
  their tests cover the refusals.
- CI: one workflow checks formatting, `go vet`, staticcheck and `govulncheck`; runs
  the guards; runs `go test` against PostgreSQL and fails on any skipped test; and
  builds the container image, runs it, and starts the Compose stack.
- Configuration: `internal/config` reads `GOBB_DSN`, `GOBB_LISTEN`, `GOBB_DATA_DIR`,
  `GOBB_BASE_PATH` and `GOBB_TRUSTED_PROXIES` from the environment. A trusted-proxy
  list with no entries is an error.
- The database: `internal/db` opens a `pgx` pool and applies numbered, embedded SQL
  migrations in order, with a checksum for each. `gobb migrate` and `gobb serve` apply
  the pending ones. `internal/dbtest` gives each test its own throwaway database.
- The release-1 schema (`0001_schema.sql`): 33 tables for members, groups, permission
  grants, forums, topics, posts, polls, attachments, private messages, watches and
  read marks, bans, ranks, smilies, legacy routes and files, sessions, settings, the
  moderation log, the email queue and static pages. Every foreign key is deferrable, so
  an import loads in one transaction. Posts, private messages and signatures store the
  three markup flags, and a new row has them on. A generated column feeds full-text
  search. `ResetSequences` moves identity counters past imported IDs.
- Passwords: `internal/password` verifies the hashes of the imported board (bcrypt,
  argon2id and the chain format, with strict limits on cost) and hashes new passwords
  with gobb's own argon2id. `Verify` says when a hash needs a rehash. Input over 4,096
  bytes never matches, and gobb does no hash work on it.
- Helpers for the importer: `password.Recognized` reports whether a stored hash
  has a form and cost that `Verify` can check. It runs no hash work.
  `account.CleanUsername` and `account.CleanEmail` give the comparison form of a
  username and an email address.
- Permissions: `internal/acl` holds the registry of 48 options and resolves YES, NO
  and NEVER, where NEVER always wins.
- The `markup` package, public for converters. It has seven parts:
  - The tree and its schema: `Node`, `Validate`, `CheckLinkURL`, `CheckImageURL` and
    `EmbedID`. A link or image target is a literal string. Nothing decodes entities in
    it, and the renderer escapes it. `Validate` refuses U+0000, which PostgreSQL
    cannot store.
  - The Markdown parser, `Parse`: CommonMark plus `||spoilers||`, bare-link autolinks
    with shortened display text, embeds for a link alone on its line, and smilies. It
    stops opening quotes and lists at 32 nested containers.
  - The renderer, `Render`: an allowlist that builds HTML by hand, checks each target
    again, renders nine embeds, and reports the frame and script origins they need.
    It also returns plain text for search, with the full address behind a shortened
    link.
  - Embeds for YouTube, Vimeo, X and Twitter posts, Instagram, Reddit, TikTok, Threads,
    Spotify and SoundCloud. SoundCloud accepts only tracks and sets.
  - Smilies: the parser matches codes; the renderer asks `RenderContext.Smiley` for the
    image and alt text.
  - `Compare` and `Diff`: a converter compares the tree it expects with `Parse` of the
    Markdown it writes, and gets the path, the wanted value and the found value of each
    difference.
  - Fuzz tests for `Parse` and `Render`.
- The dependency `github.com/yuin/goldmark` (MIT, no dependencies of its own): the
  parser behind `markup.Parse`. Spec 12.1 gives members CommonMark, which has hundreds
  of edge cases. A tested parser makes posts behave as members expect from other sites.
- The container image, Compose and the health check: a multi-stage build on Chainguard
  images, a `nonroot` user and a data volume. `compose.yaml` runs gobb and PostgreSQL 16,
  with a fixed subnet so the trusted-proxy default holds. `gobb serve` applies the
  migrations, serves `GET /healthz` and stops cleanly on SIGTERM. `gobb healthcheck`
  is the container health check.
- The import bundle format, in `docs/bundle.md` and the public `bundle` package: types
  for `manifest.json`, `settings.json` and the 23 data files, and the checks that need
  only one row (string rules, enums, ranges, exactly-one rules, the scope fit of an
  option grant, file path shape, IP addresses and ban prefixes). Every error names the
  file, the line and the field. A test keeps `docs/bundle.md` in step with the types:
  each field appears in its section, and each example line decodes and passes its checks.
- The bundle reader, writer and archives: `bundle.Open` checks the layout, `manifest.json`
  and `settings.json`. `bundle.Each` streams one data file a line at a time and refuses
  a long line, bad UTF-8, U+0000, an unknown, repeated or missing key at any depth, a
  `null` on a field that is not nullable, a failed field check and a count that
  differs from the manifest. `bundle.Create`, `Add` and `AddFile` write a bundle and
  check each row first. `bundle.Pack` and `bundle.Extract` handle `.tar.gz` files and
  refuse paths that escape, links and devices, and `Extract` reads the gzip stream to
  its end, so a bad checksum, a cut trailer and trailing data fail. `Each` refuses a CR
  line ending and a lone surrogate escape. Integer fields now stop at 2,147,483,647,
  and an email ban needs an address.
- The whole-bundle check: `bundle.Check` reads each data file once and proves the rules
  that span rows, with no database. It covers unique IDs and keys, every reference, the
  four built-in groups, an active owner, unique cleaned user names, forum parent cycles,
  topics only in forums of kind forum, moved-topic markers, poll vote limits, the scope
  fit of a role grant, legacy route targets (a user route may name a gone user) and the
  files rule. It keeps only IDs and keys, and returns up to 100 errors in file order.
  `internal/bundletest` builds a small valid board for tests, and `docs/example-bundle/`
  holds that board as a committed example. `go test ./bundle -run TestExampleBundle
  -update` regenerates it.
- The importer, part one: `importer.Run` loads a bundle (a directory or a `.tar.gz`)
  into an empty database. It holds an advisory lock on one connection for the whole run,
  checks the bundle with `bundle.Check`, refuses a database that has rows or a data
  directory that has entries (the error names the table or the directory), stages the
  files under `.import-<hex>/` while it hashes them, loads everything in one transaction
  with deferred constraints, resets the sequences, and moves the five data directories
  into place after the commit. `Replace` truncates every table and moves the old
  directories to `.replaced-<UTC time>/`. This part loads ranks, users, gone users,
  groups, members, roles, grants, forums, bans, smilies, legacy routes and files, static
  pages and the settings. Names come from `account.CleanUsername` and `CleanEmail`;
  signatures and static pages are parsed with `markup.Parse` and checked with
  `markup.Validate`, and an error names the file, the line and the ID. `db.DataTables`
  lists the tables that hold forum data.
- Permissions from the database: `internal/acldb` loads an `acl.Subject` for a user
  (or the guest, ID 0) in two queries. It adds the user's own grants and the grants of
  each confirmed group, and expands a role into one grant per option. `gobb perms
  export` writes the effective permissions of the guest and of every user as JSON
  Lines, in a fixed order, from one read-only snapshot. A converter compares the
  lines with the permissions of the source system. The command does not migrate.
- The importer, part two: it loads topics, posts, polls and their options, poll votes,
  attachments, private messages with their recipients, watches, bookmarks and read marks.
  Posts and private messages load with `CopyFrom` in batches of 1,000 rows, streamed from
  the file, so the importer never holds a file in memory. A post takes its `forum_id` from
  its topic. Each body is parsed with its own flags and the bundle's smiley list,
  validated, and stored as `body_tree`, with `body_text` from `markup.Render`. After the
  last loader, two SQL statements set the counters: a topic's last post (the newest
  approved post, or the newest post of any kind when none is approved), its `reply_count`,
  and a forum's `topic_count`, `post_count` and last post. A moved-topic marker has no
  last post. `Run` now counts all 23 data files. `bundletest.Large` builds a big board for
  the scale test: 35,000 posts import in about 36 s under `-race`.
- `gobb import [--replace] <bundle>`: it applies the migrations, calls `importer.Run`
  with the data directory from `GOBB_DATA_DIR`, and prints the row count of each data
  file and the file count. `cmd/gobb` embeds the timezone database for the static image.
  The README has an "Import a bundle" section for Docker Compose.
- A new member never gets the ID of a gone user. After an import, the `users` identity
  starts above the largest ID in `users` and in `gone_users`, so a gone user's URL keeps
  answering 410.
- A private message or an attachment may come from a gone user. `pms.jsonl` has
  `author_id?` and a required `author_name` label, and `attachments.jsonl` has
  `uploader_id?`. A message with no `author_id` needs a recipient, so that somebody can see
  it. The schema allows a NULL `pms.author_id` and `attachments.uploader_id`, and it has
  `pms.author_name`.
- `bundle.Check` needs an active owner whose `password.hash` is not empty, so that the
  board does not lock its owner out at the cutover. A name is measured on the value as
  written, not after trimming, so `bob` plus 300 spaces fails the 255-byte limit.
- A static page keeps its three body flags: `enable_markdown`, `enable_smilies` and
  `enable_autolink` in `static_pages`.
- The README "Import a bundle" section was run against a real Compose stack and corrected:
  an absolute `$BUNDLE` path, `up -d --wait` to restart, a `pg_dump` before `--replace`,
  the `.import-*` and `.replaced-*` folders to delete, and a check of the exit code of
  `perms export` and of the dump. The bundle, the dump and the export live in a folder
  outside the clone, and `.gitignore` and `.dockerignore` refuse their default names.
  A COPY batch also flushes at 8 MiB of row text, and the "import done" log line names
  the bundle's source and creation time.
- A legacy route's key is (`path`, `match`, `id_param`), so one old path can send an ID
  in one parameter to a topic and an ID in another parameter to a post. A route applies
  when its `match` pairs equal and its `id_param` is present; the lowest `position` wins.

Plan 3, the read side, one slice at a time.

- The layout, the Slate schemes and the board index. `gobb serve` now shows the board
  index to a guest: a header band with the header image (or the board name), a nav
  bar, a bar for each category, and a row for each forum with its topic and post
  counts and its last post. A link forum shows its link and no counts. Below 700 px
  the four columns stack. The page is plain server-rendered HTML; `gobb.js` only
  opens spoilers. `internal/web/static/gobb.css` holds three schemes with the same
  eleven color tokens: Slate (the spec values), Slate Deep and Slate Light.
  `?scheme=slate|slate-deep|slate-light` shows one page in that scheme and stores
  nothing; otherwise the page uses the `default_color_scheme` setting. A test parses
  the stylesheet and holds the text, muted text, link, bar and header pairs to WCAG AA
  (4.5:1) in all three.
- `internal/board` reads the board: `LoadSettings` (an error names a missing key),
  `Viewer` and `Guest`, and `Index`, which returns the forums that a viewer may list.
  A forum's counts and last post add up itself and the descendants that the viewer may
  read, so a hidden subforum adds nothing to its parent. `internal/boardtest.Load`
  gives a test a database with a `bundletest` board imported into it.
- `web.New` builds the whole handler: `/healthz` at the host root and the site under
  `GOBB_BASE_PATH`, with `site.path` as the one place that builds a site path. A path
  that is not in clean form (a `..` or an empty element) gets 404, not a redirect.
  Every page, error pages included, carries the layout. Migration `0002_read_indexes.sql`
  adds an index on topics by forum and newest activity, and one on `posts (author_id)`.
- `/files/{dir}/{name...}` serves `avatars`, `smilies`, `ranks` and `header` only. It
  opens the file through an `os.Root`, refuses any name element that starts with `.`,
  and serves a regular file only when its first 512 bytes sniff as PNG, JPEG, GIF or
  WebP. It sends that type, `nosniff` and a one-day cache. Anything else is a plain 404.
- `gobb serve` uses the server limits of the plan (header 10 s, read 30 s, write 120 s,
  idle 120 s, 64 KiB headers). On the first SIGTERM or SIGINT it stops the server,
  then closes the database pool, inside a 25 s window, and it fails with an error that
  names the pool when the pool does not close in time. A second signal exits at once.
- The forum page and the topic page. `/f/{id}` lists the topics of a forum 50 to a page: announcements and global topics first on every page, then sticky topics, then the newest activity. Each row shows the title, its markers (sticky, announcement, global, locked, moved), the author, replies, views and the last post. A category lists its forums, a link forum answers 302 to its URL, and a moved-topic marker shows only to a viewer who may read its target. `/t/{id}` shows 25 posts to a page, each in an `<article id="p{id}">` with an author block (avatar, name, rank, join date, post count; a guest shows "Guest"), the rendered body, the edit note, the attachments that the body does not show inline, the signature, and the poll results above the first page. Below 700 px the author block becomes a compact row above the body. A quote has a cite line, an embed from a video provider sits at 16:9, an image fits the width of the post, and code scrolls in its own box.
- A forum that a viewer may list but not read shows on the index and on its parent's page with a lock and "Members only", with no counts, no description and no last post. Opening it gives the page of a missing forum.
- `/p/{id}` answers 302 to the page of the topic that holds the post, as the viewer sees that page; `/t/{id}` of a moved-topic marker answers 301 to its target. Both check the permission first, so a hidden post or topic answers 404. `/a/{id}` serves an attachment with its stored type, `nosniff` and a private one-hour cache: 404 when the post or its forum is hidden, or an attachment with no post and the viewer is neither the uploader nor an admin; 403 without `account.download` or the forum's `attach.download`. It does not change the download count. A type that a browser would run on the site (anything but PNG, JPEG, GIF and WebP) goes out as a download, inside a sandbox.
- A bad `?page=` (`0`, `-1`, `abc`, more than 1,000,000) answers 400; a page after the last answers 404. One SQL clause holds the rule for who sees which posts, and a topic page runs the same number of queries for any number of posts.
- `markup.RenderContext.BasePath`: the renderer puts the base path in front of every site path it writes (a `url` href, an `img` src, a smiley image). `Rendered.Text` does not change.
- Fixes from the Task 1 review. A link forum links through `/f/{id}`, which answers 302
  to its target: a site path gets the base path in front, an `https:` or `mailto:` URL
  goes as it is, and any other value gives 404. `serveUntil` returns the shutdown error
  when a slow request outlasts the window, and gives the pool its own 5 s grace.
  `/files/` refuses a symlink even when it points inside its directory. A database with
  no imported board answers 503 with the layout and "Run `gobb import`", logs one WARN
  line, and keeps `/healthz` at 200. `serve` sets up its signal handler before it
  connects and migrates. Each request has a 10 s database deadline. A static file
  asked for with the current `?v=` is `immutable` for a year. Topic, post and view
  counts have thousands separators.
- Fixes from the Task 2 review. Migration `0003_read_indexes.sql` adds an index on `attachments (post_id)` and two partial indexes, one for global topics and one for announcements; the forum page now reads the pinned topics with one query for each. A tree image that points at `/a/{id}` shows as a note ("Image: name (sign in to view)") for a viewer who may not download, not as a broken image (`markup.RenderContext.ImageNote`). `markup` turns any `BasePath` into a rooted prefix. A moved-topic marker that points at itself answers 404. An ID with a sign or a leading zero (`/t/+5`, `/t/05`) answers 404. The author block is a plain element, the two pagers have their own names, each post has a heading (hidden when it repeats the topic title), and a test holds the heading order.
- Sign in and sign out. `GET /login` shows a card with the name (a username or an email address), the password and "Remember me". `login.Service.Check` finds the user by the cleaned username, or else by email; a shared email never picks one account (it answers "belongs to more than one account"). It verifies the password before it says anything about the account, so a stranger learns nothing from the errors, and it runs a dummy hash for an unknown name so that the time does not tell either. An inactive account answers 403, and so does a ban (a user, email or IP ban that has not ended, with its `reason_shown`). A legacy hash is rewritten as a `gobb` hash at the first good sign-in.
- Sessions (`internal/session`, migration `0004_sessions.sql`). The cookie `gobb_session` holds 32 random bytes; the table holds their SHA-256. A session lasts 24 hours, or 365 days with "Remember me", and slides when the member is seen after more than five minutes; a remembered session's cookie is sent again at that time. A session whose user is gone or inactive is a guest. Sign-out deletes the row and expires the cookie. Each sign-in records the address in `user_ips`.
- Every cookie is `HttpOnly`, `SameSite=Lax`, on the base path, and `Secure` unless `GOBB_COOKIE_SECURE=false` (for a private copy on plain `http://127.0.0.1`). Every POST passes `http.CrossOriginProtection` and a CSRF token: HMAC-SHA256 of the session token for a member, of the `gobb_csrf` cookie for a guest. A refusal answers 403. `web.ClientIP` reads `X-Forwarded-For` from the right and skips only trusted hops.
- Members see the board through their own grants: a member does not inherit the guests' grants, sees times in the zone of the member's profile and the member's color scheme, and, with the right, unapproved posts, IP addresses and attachments. A forum that a member may list and not read shows "No access", and a guest sees "Members only". The nav shows "Signed in as" with a sign-out button, and the footer has a scheme picker (`POST /scheme`): a member's choice goes into the profile, a guest's into a one-year `gobb_scheme` cookie. The schemes gain a twelfth color token, `--on-accent`, for the text on the sign-in button, and a test holds it to AA on `--accent`.
- New since your last visit. A member sees unread marks from the imported read marks. A topic is unread when its last post is newer than the newest of the member's `last_mark_at` (or `registered_at` when it is empty), the mark of its forum and the mark of the topic. There is no other cutoff. A red "New" pill marks an unread topic or forum (a forum is new when it or a forum below it, that the member may read, holds an unread topic). A member's unread names are bold and the row has a wider bar, so the mark does not rest on color; the pill uses the new tokens `--badge` and `--on-badge`, and a test holds them to AA in all three schemes. An unread topic has a "Go to the first unread post" link: `GET /t/{id}/new` answers 302 to that post on the right page (or to the last post when none is newer). `GET /new` lists the topics with a post since the last visit, 50 to a page, with the forum of each; a guest goes to sign in. The nav shows "New since your last visit (N)". `POST /f/{id}/read` ("Mark this forum read", with the forums below it that the member may read) and `POST /read` ("Mark all read") use the CSRF token and answer 303.
- Read bookkeeping happens only on a top-level navigation (`Sec-Fetch-Dest: document`; a client that sends no such header counts when it asks for `text/html`; a prefetch does not). Only then does a topic page add a view and, for a member, mark the topic read up to the newest post on that page; a page that shows only old posts leaves the newer posts unread. An image whose address is a site path changes nothing, and a test dumps every table before and after to prove it. A member's visit moves with the same rule: `last_active_at` moves at most once every 5 minutes, and a request more than 30 minutes after the last one starts a new visit (`last_visit_at` takes the old `last_active_at`); a sign-in starts a visit at once. Migration `0005_visits.sql` adds `users.last_active_at` and an index on `topics (last_post_at desc, id desc)`. The badges cost a fixed number of queries for any page size, and a page reads the marks of its own topics by primary key.
- Fixes from the Task 3 review. A session of a user who is not active is dead on every path: `session.Lookup` joins `users` and needs `state = 'active'`, and `identify` deletes the dead row, clears the cookie and uses the guest's csrf secret, so a member who goes inactive can still sign in as someone else, sign out and pick a scheme, and a POST reads the session once. A failed sign-in (wrong password, unknown name, shared address) takes at least 400 ms (`login.Service.Floor`, `Options.LoginFloor`), and a cancelled request stops waiting, so the time to answer does not tell whether a name has an account. `next` is cleaned of dot segments (plain or `%2e`) before the base path is checked. The nesting-cap test of `markup` checks growth between n and 4n, and no longer depends on the speed of the runner.
- Fixes from the Task 4 review. A mark that is not newer than the stored one (topic, forum or mark-all) writes no row version. A topic is unread only when it holds a post that the viewer sees and that is newer than the mark, so a topic whose only new post waits for approval is not new for a viewer who cannot see that post; the same holds for the forum badge and the "new since" list. The view count and the topic mark are written after the page is built, so a page that fails writes neither. `/t/{marker}/new` answers 301 to the target's `/t/{target}/new` after the permission check of the marker. The nav item "New since your last visit (N)" counts unread topics, so N falls as the member reads, and the item hides at 0. `/new` runs the scope query and the count once. The link of an unread topic or forum says "New" in its accessible name and the pill is hidden from a screen reader. The unread row tint is red, apart from the blue tint of an announcement, and a test checks the gap and the contrast of the text on both tints in all three schemes. `RecordVisit` and `StartVisit` skip a user who is not active.
- Profiles, the member list, the team and static pages. `GET /u/{id}` shows a card: the avatar, the name with its rank image, a facts list (posts, joined, last visit, groups), the signature and a "Find posts by this user" link to `/search?author=<name>`. A removed user answers 410 "This account no longer exists." before any permission check; an unknown or inactive user answers 404; a viewer without `account.view_profiles` gets 403, with a sign-in link that returns to the page for a guest. No page shows an email address, an IP address or a hidden group, to anyone, and a test with a hidden group holds that for a guest, a member, a moderator and an admin. `GET /members` lists the active members 50 to a page with sortable heads (`name`, `joined`, `posts`, as links; the sort stays in the page links) and stacks the rows on a phone; it costs two statements for any number of members. `GET /team` lists the owners, then the admins, then the moderators, each person once under the first heading that holds them. `GET /pages/{slug}` shows a static page at the reading width of a post, and the footer of every page lists the static pages. `GET /account` goes to sign in for a guest and to the member's own profile otherwise. The nav gains Members (with `account.view_profiles`) and Team. Every author name already links to `/u/{id}`; a gone author has no link.
- Search. `GET /search?q=&forum=&author=&sort=&page=` finds posts by the words in the post or in the title of its topic (a title matches the first post of the topic), narrowed by forum and by author, newest first or best match first. It covers only forums where the viewer has `forum.read` and `forum.search`, and only approved posts in approved topics that are not markers; a viewer without `account.search` gets 403 before any other check. A text or author over 256 bytes, one that is not valid UTF-8 or holds a NUL byte, or a text of more than 8 terms answers 400 with the form and a message; a text with no positive term (`-word` alone) shows the form and "Add a word to find."; the text is parsed once, so a cached generic plan costs no more per row; a post and its topic must both sit in a forum the viewer may search; a page past 40 answers 404, and the total stops at 1,000 so a common word costs no more than a rare one. A search runs the forum list, the total and the page of hits; `ts_headline` runs on the 25 hits of the page only, the snippet is escaped, and the matched words sit in `<mark>` (new `--mark` and `--on-mark` tokens, held to AA in all three schemes). The nav shows a search box and an Advanced link for a viewer with `account.search`; the results page has the form (words, forum, author, sort) and the "Find posts by this user" link of a profile now works. Migration `0006_search.sql` adds a GIN index on the title text of topics and an index of approved posts by author and time.
- Fixes from the Task 5 review. An inactive member's profile opens like an active one's (the member cannot sign in, but the posts are on the board); the member list still shows active members only, and a removed user still answers 410. The page that marks a topic read counts the nav "New since your last visit" after the mark is written, so it no longer counts the topic it just marked. A test holds that a moderator sees another member's unapproved post as New. The member list sorts and limits first and looks up ranks for the page's rows only (a plan test counts the lookups). A static page with no title shows its slug in the window title.
- Legacy routes and file paths (`internal/legacy`). `web.New` reads `legacy_routes` and `legacy_files` once into memory and logs one INFO line with the counts; a request logs nothing. A request that no route of the site matched runs the table. A route applies when the path under the base path (decoded once, no leading slash, no trailing-slash fold) equals its `path`, each `match` pair equals the first value of that query parameter (the parameter must be present), and the query has the `id_param` (an empty value counts as present). The lowest `position` wins, then the lowest row ID. File prefixes are found by probing the slashes of the path against a map, so the work does not grow with the number of prefixes. The answers are 301s: `home`, `members`, `team`, `search` and `account` go to their pages; `forum` and `topic` go to `/f/{id}` and `/t/{id}` (with `?page=` when the offset divided by 50, or by 25 for a topic, is 1 or more); `post` goes to `/p/{id}`; `user` goes to `/u/{id}`, with 410 at once for a gone user; `attachment` goes to `/a/{id}`; `avatar` goes to the file of the avatar; `page` goes to `/pages/{slug}`. Each kind runs the check of its own page first (`ForumView`, `TopicView`, `PostLocation`, `Profile`, `AttachmentFile`), so a hidden target answers the same 404 as a missing one, and a refusal such as the 403 of an attachment is the page's. A moved-topic marker goes straight to its target when the viewer sees the target. An ID that is not a positive integer, an offset that is not a whole number of 0 or more, and a page above 1,000,000 answer 404. A legacy file goes to `/files/<dir>/<rest>` only when `/files/` would serve it, and an avatar link only when its file serves. `web.New` logs a WARN for each route or file prefix that a route of the site answers first. `TestHiddenForumNeverLeaks` holds that a guest and a member without `forum.read` learn nothing of a hidden forum from the pages, from search, from a global topic or from an old link.
- A link forum whose target starts with `http://` answers 302 to the `https://` form, as the renderer does for an image.
- Fixes from the Task 7 review. A search text holds at most 8 terms (it was 16): the worst case, the eight most common words joined by `or` and sorted best, takes about 62 ms on 32,000 posts, where the 16-term case took about 91 ms. `web.New` also warns for a legacy route or file prefix whose path is not clean (the guard answers 404 first) and, at the host root, for the route `healthz`. `/f/{id}` and the old `forum` link read the scheme of a link forum in any case (`HTTPS://`, `Mailto:`, `HtTp://`), and the old link answers 404 when the page would. The `avatar` kind leads to the `avatars/` directory only. `TestHiddenForumNeverLeaks` compares the headers of a hidden answer with those of a missing one.
- Login limits. Ten failed sign-ins from one address in 15 minutes, or 20 for one name in an hour (the name is cleaned, and counts whether or not an account has it), make the next try answer 429 with `Retry-After` and "Too many sign-in attempts. Try again in N minutes." on the sign-in page, with the form still there; N rounds up, so it never reads "0 minutes". A limited try runs no password check and does not wait for the 400 ms floor (when only the name limit is over, a lookup of the name and one query on `user_ips` run first; no hash does); a good password clears the name's count. Counts live in memory (`internal/ratelimit`, at most 10,000 keys for each limit) and start again at zero when the server restarts. Password checks share a 128 MiB gate (`login.Gate`): each check holds the memory its hash needs (`password.MemoryKiB`: the `m` of an argon2id hash, the costliest layer of a chain, 1,024 KiB for anything else), the dummy check for an unknown name goes through the same gate, a check that cannot get room in 5 seconds answers 503 with `Retry-After: 5`, and the 400 ms floor is waited after the gate is free. Request bodies are capped before the CSRF check: 16 KiB for `POST /login`, 4 KiB for every other POST, and a larger body answers 413 in the layout. `gobb serve` deletes expired sessions and `user_ips` rows last seen more than 90 days ago once at start and then every hour (`session.Cleanup`), and logs the two counts.
- Security headers and a per-page Content-Security-Policy (`internal/web/headers.go`). One middleware around the whole handler puts `X-Content-Type-Options`, `Referrer-Policy`, `X-Frame-Options`, `Cross-Origin-Opener-Policy`, `Permissions-Policy` and the base policy on every response: errors, redirects, `/healthz`, files and the 413 and 429 answers. The base policy allows nothing but the site itself and `https:` images. A page that renders posts, signatures or a static page adds the frame, script and connect origins of the embeds on it, one `<script async>` for each provider script, and `'unsafe-inline'` for styles when a provider script is on the page (the X script sets style attributes). `markup.Rendered` gains `ConnectSrc` and `Scripts`; the embed table holds them with the rest of each entry. The `/a/` download of a non-image keeps its sandbox and adds it to the page policy. An HTML page sends `Cache-Control: private, no-cache` to a guest and `private, no-store` to a member. `/login`, `/new`, `/search` and the error pages carry `<meta name="robots" content="noindex">`. A test scans the templates for inline script and style.
- Fixes from the Task 8 review. The sign-in limits reserve before the hash: `Check` takes one event from the address limit and one from the name limit in one step under each lock (`ratelimit.Limiter.Take`), so parallel guesses cannot pass a limit that the first of them has not reached (a test sends 50 at once and counts the hashes). A good password, a busy gate, an inactive account, a ban and a database or context error give the events back (`Refund`); a good password also clears the name. A client on IPv6 counts as its /64; IPv4 stays per address, and an IPv4-mapped address is the IPv4 address. When a limiter is full, it drops expired keys first, then the key with the fewest events (oldest last event among equals), so a flood of junk names no longer frees a locked name. When only the name limit is over and the address is one that the account has used before (`user_ips`, matched by the same /64 rule), the name limit does not apply to that try; the address limit still does, and the lookup runs for every name, with or without an account. The longer wait wins when both limits are over. The rehash of a legacy hash runs inside the memory gate, and a busy gate skips it (the next sign-in tries again). Migration `0007_user_ips_last_seen.sql` indexes `user_ips (last_seen_at)` for the hourly delete. The name limit counts the name as typed (after cleaning), so a username and an email address give one account two budgets, and the counts start at zero when the server restarts; the README says so.
- Fixes from the Task 9 review. Every response, a redirect included, carries `Cache-Control`: the middleware sets `private, no-cache` (`private, no-store` when the request has a session cookie), a page replaces it with the value for its viewer, and `/static/`, `/files/` and `/a/` keep their own, so a shared cache cannot keep a moved topic's 301 and show it to someone else. Reddit renders as a plain "View post on Reddit" link, with no script and no frame origin. An error that is not a page (the plain-text fallback, `/healthz` 503 and 405) carries `X-Robots-Tag: noindex`. The "Embedded post" fallback of a Threads or Instagram embed hides once the provider frame is there. The template scan is case-insensitive and treats `data-src` as no `src`.
- The image serves pages under a 256 MiB memory cap, checked in CI. `.github/compose.memcap.yaml` sets `mem_limit: 256m` and `memswap_limit: 256m` on `gobb` (no swap on top of the cap) and `GOBB_COOKIE_SECURE: "false"`. The `image` job starts the stack, stops `gobb`, imports `docs/example-bundle/`, starts it again (the legacy routes load at start), and runs `scripts/smoke-serve.sh`. The script checks the status of `/`, `/f/2`, `/t/1`, `/login`, `/t/3` (404 for a guest, 301 for a member), `/u/8` (410), `/search` (403 for a guest, 200 for a member), two old addresses (301) and the sign-in of `alice`, and a `Content-Security-Policy` header on each page. It then reads the cap, the restart count and the OOM flag of the container, prints its memory use, and times `docker compose stop gobb` and checks that the exit code is 0. All job names carry the run ID and attempt, and the clean-up step runs on failure. `shutdownTimeout` is 20 s (it was 25 s), so it and the 5 s `poolGrace` fit in the 30 s `stop_grace_period`. The member role of the small test board and of the example bundle gains `account.search`. The README gains "Sign in" and "Old addresses", and says that an empty database answers 503.
- Fixes from the second Task 9 review. The CSS no longer hides a blockquote by the iframe before it, which hid a member's own quote after an Instagram embed (the Instagram script removes its own fallback); the Threads fallback hides by its own class, `text-post-media-registered`, and a test refuses any sibling selector that can match a blockquote. `/a/` sends `private, no-cache` and `Last-Modified`, so a browser asks again and gets 304 while the file is unchanged.
- Fixes from the Task 10 review. The CI smoke run now asserts what it printed: `stop gobb` takes under 25 s and exits 0, and the script must fail (exit 1) on the empty board before the import. The job reads the container's cgroup `memory.peak` and the `oom_kill` count of `memory.events` before the stop (systemd path, then cgroupfs path; it prints the path used, fails on `oom_kill` above 0, and falls back to `docker stats` when neither file is readable). The smoke search looks for a link to a post, not the echoed query; the member group of the small board and the example bundle gains `forum.search` on the main forum so that a member finds a hit. `scripts/smoke-serve.sh` uses POSIX `grep -e`, matches the whole `Location` line, and exits on INT and TERM. A test holds `shutdownTimeout + poolGrace` under the `stop_grace_period` that it reads from `compose.yaml`.
- Fixes from the Task 8 re-review. A failed sign-in always takes the 400 ms floor, measured from the start of the call, and the wait does not watch the request context: net/http cancels that context when a client half-closes its connection, and the client can still read the answer, so a floor that ended with the context let it time the answer to the hash alone. A try that the name limit refuses keeps its address event, so one address cannot send unlimited lookups at a locked name. A try that the address limit refuses takes nothing from the name limit. The unspecified address (`0.0.0.0`, `::`) is never a known address, and `session.Create` does not store it in `user_ips`. A full limiter drops a batch of about 1% of its keys in one scan instead of one key per scan (a new key into 10,000 live keys: about 140 microseconds before, 2.4 after). `LimitedError` and the Task 8 bullet now say what runs before a refusal. Round 3: every password check, and the dummy check for an unknown name, charges the gate the same (`max` of the memory of a new gobb hash, 19,456 KiB, and the memory of the stored hash), so a legacy account, a gobb account and an unknown name wait for the gate alike; a stored hash above that charge still charged its own cost (corrected in round 4). Every failure after the limits pass (a busy gate, a gate wait that the request context ended, an inactive account, a ban, a database error) now takes the 400 ms floor; a good password and a limited try still answer at once. Round 4: the charge follows the most costly stored hash. Round 3 said that a hash above the current parameters comes only from an operator who lowers them; that was false, because imported argon2id hashes commonly use m=65,536, and under a full gate such an account answered 503 or 500 where an unknown name answered 200. `web.New` now calls `login.Service.LoadCharge`: one streaming query reads the users whose hash can carry an argon2id part, `password.MemoryKiB` sizes each one, and every check (the dummy check and the rehash too) charges the larger of 19,456 KiB and the most costly stored hash. On a board with imported 64 MiB hashes, two checks run at once in the 128 MiB gate. One INFO line at start gives the charge in KiB and the number of checks at once, with no name and no hash. A hash that an import adds to the database of a running server charges its own cost when that is higher, until the next start. Round 5: a sign-in takes the gate once, before any hash, and holds it across every account that the name finds (a shared email address) or across the dummy check; the hold is the largest charge among them. A gate wait, a 503 and a cancel of the request context now happen once, at the same point, for every name, so another sign-in that takes the gate between two accounts of a shared email can no longer turn that email's 200 into a 503 or a 500. The floor of a failed sign-in is now set at start (`login.Service.LoadFloor`): 2 × K × T, at least 400 ms, rounded up to 50 ms, where T is the slowest single wrong-password check over the cost shapes of the stored hashes and the dummy check (`password.CostShape`: the format and its cost parameters), and K is the most accounts that share one `email_clean`. One INFO line at start gives the floor, T and K. The floor hides the hash time while the host runs a check within twice its start-up time; heavier CPU load can still show it. A test that sets `web.Options.LoginFloor` skips the timing.
- Fixes from the final review: memory. A burst of wrong passwords against imported 64 MiB argon2id hashes killed `gobb` under the 256 MiB cap, and each restart cleared the sign-in limits: the gate held two 64 MiB checks, but the Go heap grew to twice that before it collected. At start, `gobb serve` now sets the Go memory limit (`internal/memlimit`): `GOMEMLIMIT` when it is set, else 70% of the cgroup memory limit (v2 `memory.max`, or v1 `memory.limit_in_bytes`, the least on the path of the cgroup), else none. The gate budget follows it: min(128 MiB, the limit − 48 MiB), at least 1 MiB (`login.BudgetKiB`). One INFO line, `memory limit`, gives the limit, its source and the budget. A check now gives its share of the gate back only after a garbage collection: with the memory limit alone, the heap still held three 64 MiB blocks where the gate allows two, and the container was killed once in two local runs; with the collection, the cgroup peak was 137 MiB in 3 of 3 bursts of 40. A stored hash above the gate budget gets one WARN line at start, with the number of such accounts; such a check runs alone, and the README says to raise the cap for such a board. `LoadFloor` holds the gate (`chargeKiB`) for each timing check, times each with a 4,096-byte wrong password (a chain check grows with the length of the password), and runs a garbage collection after the last. The floor of a failed sign-in counts from the later of its start and the moment the gate lets it in, so a gate wait adds the same delay to every name. `compose.yaml` runs `gobb` under `mem_limit` and `memswap_limit` of `${GOBB_MEM_LIMIT:-256m}`, the cap that CI tests; `.env.example` and the README name `GOBB_MEM_LIMIT`, and the README gains "Memory". `TestLoginMemoryBudget` now claims only the concurrency that it tests. A `TestLoadCharge` row now uses a chain that `VerifyLegacy` accepts (no accepted chain holds an argon2id layer), and the `TestCostShape` comment says what its loop checks.
- Fixes from the final review: pages. A profile's "Last visit" is the last time the member was on the board (`last_active_at`, or the imported `last_visit_at` when the member has not been seen since), not the start of the "new since" window. "Sign out" with no live session answers 303 to the index: there is nothing to sign out of. Any other form of a member's page sent after the session ended answers 403 "Your session ended. You are signed out." in place of the advice to reload; a live member's wrong token and a guest's still get the plain refusal. A forum that the viewer may list but not read answers 403, with a sign-in link that returns to the forum for a guest (its name is already on the index); its old link answers the same. A forum that the viewer may not list, and every topic, post and attachment in a forum that the viewer may not read, still answer the 404 of a missing one. "Find posts by this user" shows only to a viewer with `account.search`. The site has an icon of its own, `/static/icon.svg` (a speech bubble on the accent blue), linked from every page; `/favicon.ico` at the host root answers with it and does no database work, and a legacy route named `favicon.ico` warns at start. A topic page no longer reads the last posts of the forum tree (`index` gains `withLast` beside `withNew`). The scheme picker's write moves to `board.SetScheme`. Notes: the rule that a GET writes nothing has one accepted exception, a GET that deletes the row of a dead session, and the code and the side-effect test say so; the `TestGlobalTopicsQueryUsesIndex` comment no longer claims "no sort". A new test holds that an `If-Modified-Since` request for `/a/{id}` from a viewer without the right gets 403 or 404, never 304.
- Every error page is themed (spec 13, the owner's request in the final fix). With the database down or slow, past the request deadline, or with a template that fails, a page no longer answers in plain text. The site keeps a copy of the settings that the layout needs (`site.frame`: the board name, the header image, the default scheme and the zone), loaded in `New` and replaced by any page that reads changed settings. When the page of the viewer cannot be built, the error page is a guest's page from that copy (`site.framePage`): no session lookup, no nav counts, no footer query, no form. When a page template or the csrf secret fails, the answer is the 500 (or 503) of the error template, from the same copy; when the error template fails too, it is the themed 500 or 503 that `New` built at start (`site.staticErrors`). These answers send `Cache-Control: no-store`, and every such page carries the robots meta. `TestNoindexHeaderOnPlainErrors` no longer skips on a page: a request that cannot read the settings must get that page. Under a base path, a path outside it answers the themed 404 instead of the mux's plain text. A test scans the package: no handler but `/healthz` calls `http.Error` or `http.NotFound`.
- Fixes from the final review: search and markup tests. The search tests gain a quoted phrase that takes the query one word over the cap (`ErrSearchTooManyTerms`, and 400 on the page), a post and a first post in a hidden forum under a readable topic, found by author alone and through the title (each case fails when `p.forum_id` or `fp.forum_id` leaves its arm), and a member who reads a forum but may not search it and finds nothing there, on the board and on the page (both fail when the `forum.search` check goes). `TestLinkTarget` refuses `data:` URLs in any case. The CSS scanner for sibling rules sees a combinator inside `:is()`, `:not()` and `:nth-child(… of S)`, splits a selector list only at top-level commas, and no longer takes the `+` of An+B or an attribute's `~=` for a combinator. Two overlong doc comments are wrapped. No behavior changes.
- CI tests the memory cap with a burst of sign-ins. After the import, `.github/memcap-hashes.sql` gives `bob` and `carol` a 64 MiB argon2id hash (m=65536, t=4, p=2) of a made-up password, and the step wants `UPDATE 2`. The start-up log must hold the memory limit from the cgroup (`limit_bytes=187904819 source=cgroup gate_budget_kib=131072`) and the charge `kib=65536 checks_at_once=2`. The new step "A burst of wrong passwords under the memory cap" runs `scripts/login-burst.sh` (40 wrong passwords, 20 at a time, each from its own address in `X-Forwarded-For`, 20 for each name) and wants every try to answer 200, a restart count of 0 and no OOM kill, `oom_kill 0` in the cgroup's `memory.events`, and a `memory.peak` under 256 MiB. `.github/compose.ci.yaml` trusts every IPv4 peer as a proxy for that (CI only). On 5875a34 the same burst killed gobb in each of three runs; with the fix, the peak was 137 MiB. The empty-board step now also wants the line `FAIL /: want status 200, got 503`, since a curl failure exits 1 too. `scripts/burst_test.go` tests the script against a fake server: all 200 passes; a 429, a 503 or a dropped connection fails; a usage error exits 2.
- Docs from the final review. The README's Status says what the read side serves, and that posting is Plan 4. The roadmap gains "Carried from Plan 3": issues #2 (load protection), #3 (crawlers) and #4 (a new board without an import) as work before the write side or beside it; the quadratic `[a](` parse as a must-fix before posting ships; `TestParseNestingScales`, which rests mostly on an absolute bound; text only inside angle brackets, which the search column misses; the slug pattern in the static-page editor; a ban that deletes the member's sessions; and a bound on the "best" sort at about 10 times today's posts.
- Fixes from the read side's last review (Plan 4, Task 0). A sign-in that holds the gate for several checks (a shared email address) now collects garbage before each check after the first (`runJobs`), so the memory of a finished check is free before the next one starts, as the README says; with three 64 MiB hashes on one email, the heap held up to three blocks (192 MiB at the Go level, 200 MiB in the container) where the gate allows two. The floor of a failed sign-in now counts from the start, moved forward by the time spent waiting for the gate and by nothing else (`clock.waited`), on every return of the gate (a busy answer and a wait that the request context ended too), so the lookup is under the floor again: since the floor counted from the grant, a missing name answered about 0.1 ms after an existing one. `TestHoldEndsWithACollection` now sees a collection that runs after the release (a waiter records the count when the gate lets it in), `TestLoadFloorHoldsTheGate` now sees the collection after the last timing check (it counts the forced collections of the step), and three new tests pin the collection between checks (`TestHoldCollectsBetweenChecks`) and the floor over a 5 ms slower lookup (`TestFloorCoversTheLookup`, and `TestFloorCoversTheLookupWhenBusy` for the busy answer). `http.ServeContent`'s plain-text 416 for a `Range` that no byte fits (`/static/`, `/favicon.ico`, `/files/`, `/a/`) and `http.StripPrefix`'s plain-text 404 for a percent-encoded letter of the base path now answer in the layout (`site.serveContent`, `site.stripBase`); `TestNoPlainTextErrors` covers both and refuses `http.StripPrefix`, `ServeFile`, `FileServer` and a `ServeContent` call outside `site.serveContent`.
- Markup hardened for writes (Plan 4, Task 1). The parse of a repeated `[a](` was quadratic: goldmark read the destination of every `](` to the end of the line, so 10,000 repeats took about half a second. The link parser now runs behind a scan budget (`markup/linkscan.go`): a parse may read 32 bytes of destination for each byte of source, plus 64 KiB, and past that a `](` is not a link. A natural post never reaches the budget; the result depends on the input only. 10,000 repeats now take about 8 ms, and the cost grows in step with the input. A second quadratic was in `addText`, which joined text nodes with `+=` (40,000 `[` took 70 ms; it is linear now). `TestParseLinkRunLinear` and the rewritten `TestParseNestingScales` fail for quadratic growth (twice the input must take less than three times as long; no additive term). Search finds words that stand only inside angle brackets: the PostgreSQL text search parser reads `<like this>` as a markup tag and drops it. Migration `0008_search_brackets.sql` maps `<` and `>` to spaces inside the search expressions only, for the `posts.search` column (subject and `body_text`, so the posts already stored become searchable with no reimport; PostgreSQL 16 cannot change a generated expression in place, so the column is dropped and added again, and `posts_search_idx` is created again) and for `topics_title_search_idx`; the title query in `internal/board/search.go` writes the same expression (`titleVector`). `Rendered.Text` and `body_text` keep the brackets as written, so a snippet shows `a < b` and `x -> y` as they were. `markup.Allowed()` is the table of every element and attribute that the renderer may write, and the renderer writes every tag through it (the output is byte-identical; a tag outside the table is not written). `markup.EmbedOrigins()` lists the frame origins. `internal/htmlcheck.Check` reads both tables and fails on any element or attribute outside them, any `on*` or `style` attribute, an `href` or `src` that fails the URL rules (with a second, independent scheme check), an `iframe` outside the embed origins, the forbidden elements (`script`, `style`, `object`, `embed`, `base`, `form`, `meta`, `link`), comments, doctypes, incomplete tags, repeated attributes and a raw angle bracket in text. `markup/testdata/xss/payloads.txt` holds 871 payloads in 16 categories, and `TestPayloadsRenderSafe` parses each with all eight mixes of the flags, renders it and checks the HTML. `FuzzParse` and `FuzzRender` moved to an external test package (`markup_test`, because the check imports `markup`) and call the check. CI gains a `fuzz` job that runs `FuzzParse`, `FuzzRender` and `FuzzVerifyLegacy` for 60 s each through `scripts/fuzz.sh`, which lists each target first and fails when a name matches no fuzz test (`go test -fuzz` alone exits 0 with a warning), and uploads the failing inputs. The check also pins every attribute that the renderer writes as a constant to its values (`markup.AttrValueOK`) and requires the tags to balance. The one new dependency is `golang.org/x/net` (v0.61.0), for `golang.org/x/net/html` in the check; only test files import the check, and `TestNotInTheBinary` fails if the `gobb` binary ever depends on it. Its requirement moved `golang.org/x/crypto` from v0.43.0 to v0.58.0.
- The board write functions (Plan 4, Task 2). `internal/board` can now start a topic (`NewTopic`), reply (`Reply`), edit (`EditPost`), delete, restore and approve a post (`DeletePost`, `RestorePost`, `ApprovePost`), lock and unlock a topic (`SetTopicLock`) and build a quote (`QuoteSource`). Each write runs in one transaction, checks the rights of "Who may do what" itself (a target the viewer cannot read is `ErrNotFound`, one the viewer can read but may not change is `ErrForbidden`, a problem in the text is an `*InputError` with a field and a message for the writer, and no database text reaches a caller as a message), keeps the counters exact and writes the moderation log. `ParseBody` is the one path from member text to a stored tree: it parses with the flags of the writer's rights in the forum (a new post) or the stored flags (an edit, and a signature with forum 0), checks the tree with `markup.Validate`, refuses a text over 60,000 bytes (2,000 for a signature), an empty text, invalid UTF-8, a NUL byte and an image without `post.images`, and returns the tree, its JSON and its plain text. The import and the write path share `internal/postbody.Columns`, and `TestParseBodyMatchesImport` re-parses every fixture post with its stored flags and compares tree and text with what the import stored. `internal/counters` now holds the two recompute statements, moved from the importer, as one text each with marks where the one-row forms (`Topic`, `Forum`) add a filter, so the whole-board form (`All`) and the one-row forms cannot differ; `TestCountersOneRowEqualsAll` makes random visibility changes on a 12-topic board and compares the one-row result with `All`, and `TestCountersOneRowTouchesOneRow` pins that a one-row form changes no other row. Locks: a write takes a transaction-scoped advisory lock on the writer (the member, or for a guest the address; `NewTopic` and `Reply` only), then the row lock of the topic, then (for the writes that change a post) the row lock of the post, then the row lock of the forum when a counter of the forum changes (`NewTopic` takes only the forum). The row locks are `for no key update`, so the foreign-key checks of the topic marks do not wait for a write. The counters statements read with the snapshot of their own start, so only a lock taken before them keeps a late writer from storing a number that a concurrent writer has made stale; `TestConcurrentWritesKeepCountersExact` runs 24 mixed writes at once for four rounds and compares with `counters.All`, and fails with a deadlock or a drift when the locks are removed. A post, or a new topic, with the same author, topic (forum and title for a topic) and text as one from the last 60 seconds is not saved again, and the write returns the earlier post (`TestDuplicatePostIsOne`, `TestDuplicatePostRace`, `TestDuplicateTopicRace`). A guest writes where the guests group holds `topic.start` or `topic.reply`; the post is held unless the group also holds `post.no_approval`; a name whose `account.CleanUsername` equals a member's `username_clean` is refused; a guest can never edit, delete or moderate, and a guest from an address that an unexpired `ip` ban holds gets `ErrForbidden`. Deleting the first post of a topic deletes the topic, and restoring it brings the topic back; a delete keeps the state of the row in `visibility_before_delete` (migration `0009_visibility_before_delete.sql`, on `posts` and `topics`), and a restore puts it back, so a held post stays held; a row with no kept state (deleted before the migration, or imported as deleted) comes back approved only when the restorer holds `mod.approve` in the forum, and held otherwise; an approval of the first post of a held topic approves the topic, also when the post is approved already. `users.post_count` moves with the approved posts of an author who holds `post.counts` in the forum, by the right of the author and not the moderator, and never below 0. A moderator's edit, delete, restore, approve, lock and unlock each write one `mod_log` row (`details` is `{"reason":"..."}` for an edit and `{}` otherwise); an author's own edit and delete write none; a write that changes nothing writes none. `QuoteSource` quotes only an approved post in an approved topic, even for a moderator who sees the hidden ones, and needs the reply rights (including an unlocked topic and forum, or `mod.lock`) for the topic of the quoted post; the caller still checks the target topic. A guest name must survive `account.CleanUsername` but for case (`account.PlainName`): a name with a right-to-left override, a zero-width joiner, a soft hyphen or a full-width letter is refused with "Use letters, digits, spaces and common punctuation only." A signature may hold images when the member holds `post.images` in some forum, and an edit by a writer without `post.images` may keep the image addresses that the stored post holds and add none (`ParseBodyKeeping`). A moderator right that is the only one to allow an action on the own post (deleting the own first post with replies, editing the own post in a locked topic) writes a `mod_log` row; the own right alone writes none, even for an author who also holds the moderator right. An author with only `topic.lock_own` unlocks a topic only when the latest `topic.lock` row names the author. Every new post needs the address of the writer, member or guest.
- Posting pages (Plan 4, Task 3). The site can now take a post: `GET`/`POST /f/{id}/post` (new topic), `/t/{id}/reply` (reply, with `?quote={post}`), `/p/{id}/edit` and `POST /p/{id}/delete`. Each POST goes through `site.protectPosting` (Cross-Origin Protection and the csrf token; a refusal answers "This form expired" and never echoes the request) with a cap of 256 KiB (4 KiB for a delete; a body of 60,000 bytes of 3-byte characters percent-encodes to 180,000 bytes), and answers 303 to `/p/{id}`. A form page calls the check of the board first (`board.CanStartTopic`, `board.CanReply`, `board.LoadEdit`, `board.LoadDelete`, which share their rules with the writes), so a viewer sees a form only where the write would pass; a post that the viewer cannot see, or a hidden post named in `?quote=`, gives the 404 of a missing one and never puts its text into the form. `board.QuoteSource` no longer checks the lock of the quoted post's own topic and forum: the lock belongs to the topic that the reply goes into, which the reply handler checks with `board.CanReply`. Delete is a POST with a confirm step that needs no script: the first POST answers 200 with "Delete this post?" and a second form with `confirm=1`, and changes nothing. After a delete the answer is 303 to the topic, or to the forum when the topic is gone for the viewer. The form keeps what the member sent byte for byte on every 400, 403 and preview, `\r\n` and a leading line break included (`textareaValue` writes a carriage return as a character reference and adds the line break that a parser drops). A preview runs `board.ParseBody` (`ParseBodyKeeping` with the stored flags and tree for an edit, as `EditPost` does) and renders through the same `post` template as the topic page, with the writer's author card; it saves nothing. A guest sees "Your name" where the guests group may post; the name shows as plain text with no profile link and the Guest label. A guest post that waits for approval answers 303 to the topic or forum with `?held=1`, which shows a note, because `/p/{id}` would be a 404 for the guest. The buttons (New topic, Reply at the top and bottom, Quote, Edit, Delete) show only where the viewer holds the right; the board functions check again. `board.ForumHead`, `board.TopicHead` and `board.Card` give the heading and the author card of a form. The title message is now "The title is longer than 120 characters.". Tests: `TestPostingFlow`, `TestQuoteHiddenPostIsNotFound`, `TestFormKeepsTextOnError`, `TestPreviewSavesNothing`, `TestPreviewOfEditAgreesWithSave`, `TestPostingNeedsToken`, `TestGuestPostingPage`, `TestCSPStrictEverywhere`, and `TestPayloadsThroughPosting`, which posts every line of the script-injection corpus through the form and runs `htmlcheck.Check` on the raw bytes of each `.post-body` on the previews and on every page of the topic.
- Fixes from the Task 3 review. A moved-topic marker is never a reply target: `CanReply`, `TopicHead` and `Reply` answer `ErrNotFound` for one, and every topic read of the writes answers `ErrNotFound` for a marker whose target the viewer cannot see, as `TopicView` does, so `/t/{marker}/reply` is the 404 of a missing topic and shows no title. A quote needs only that the viewer reads the quoted post (`QuoteSource` no longer asks for `topic.reply` in its forum); the rights to reply belong to the target topic. The text of a post never gets lost to a failure after the token passed: a database error or a timeout during a save or a preview answers 500 (503 for a timeout) with the form, the exact bytes, and a fixed notice with no detail; a topic or forum that is gone answers 404 with a bare form that holds only the text; a failed token or cross-origin check on a posting form answers 403 "This form expired..." and never echoes the body. A preview runs the title, guest-name and reason checks of the save (`board.CheckDraft`). The rules for the buttons and for downloads live in `internal/board` (`TopicPage.CanReply`, `TopicPage.Buttons`, `ForumPage.CanStart`, `MayDownload`), and the web package keeps no copy. The edit fallback goes to the topic of the post. A delete that committed always answers 303 (topic, else forum, else index). The payload test cuts each `.post-body` by depth and checks the markup that follows it, and compares every directive of the Content-Security-Policy with a page that has no payloads. The cap of the posting forms is 256 KiB.
- Fixes from the Task 3 re-review. `markup.EmbedScriptOrigins()` and `markup.EmbedConnectOrigins()` export the script and connect origins of the embed table beside `EmbedOrigins()`; the rows of the table read the same lists, and a test pins them to what `richSource` renders. The posting tests check every `script-src` and `connect-src` token of a page with an embed against them, so an origin that the table lacks (or a frame-only host in `script-src`) fails. The "This form expired" page is built from the settings in memory (`framePage`), so it carries no path, no query, and no sign-in link from the request.
- The sign-in floor timing tests (`TestFloorCoversTheLookup`, `TestFloorCoversTheLookupWhenBusy`) no longer flake under load. The lookup gap is 20 ms (was 5 ms), each name runs 8 and 6 trials, and the tests compare the least answer time of each name within 5 ms (was 1 ms). A floor that leaks the lookup shows a difference of about 20 ms and fails; scheduler jitter of a few milliseconds passes.
- Moderation in place (Plan 4, Task 4). The topic page shows the moderator controls to a viewer who holds the right. Each is its own small POST form with the token, and no script. A post offers "Delete" (the same two-step confirm as an author's delete), or "Restore" when it is deleted (`POST /p/{id}/restore`, `mod.delete`), and "Approve" when it is held, or is the first post of a held topic (`POST /p/{id}/approve`, `mod.approve`). The topic offers "Lock" or "Unlock" (`POST /t/{id}/lock` and `/unlock`; `mod.lock`, or `topic.lock_own` for the author, who unlocks only the author's own lock). Restore, approve and lock answer 303 (to the post, or to the topic when the viewer cannot see the post any more; to the topic for a lock), and a hidden or missing target answers the same 404. The rules for the buttons live in `internal/board`: `PostButtons.Restore` and `Approve`, `TopicPage.CanLock` and `CanUnlock`, and `lockRights`, which `SetTopicLock` now calls too, so the button and the write cannot disagree. A reply that a lock stops now says which lock: `ErrTopicLocked` and `ErrForumLocked` are an `ErrForbidden`, and the form answers 403 with "This topic is locked." or "This forum is locked." and the text the member sent. The edit form labels the reason field "Edit reason"; the edit note shows the reason as text, so a reason that holds `<script>` shows as typed. The new routes have rows in `bodyCaps` and join `TestPostingNeedsToken` and the base-path test.
- Fixes from the Task 4 review. `TestPostingNeedsToken` now sends each row as a member who holds every right the request needs (a moderator for restore, approve, lock, unlock and the moderator delete; a guest with the grants for the guest rows), first with a good token to show the request succeeds, then with no token, a wrong token, a guest's token and a cross-site origin. Dropping `s.protect` from any one of the eight member routes now fails the test; before, a request without the rights was refused by the rights check and passed whatever the token did. A restore whose post and topic stay hidden from the restorer (the topic comes back held, and the restorer lacks `mod.approve`) answers 303 to the forum, then the index, as a confirmed delete does (`fallbackTarget`, `board.PostPlace`). The edit form asks for a reason only when the viewer edits another member's post: a moderator who edits the own post by `mod.edit` alone is not asked, a reason sent anyway is not checked, stored or logged, and the `post.edit` row has an empty one. `TestModeratorControls` compares the stored counters with `counters.All` (in a rolled-back transaction) and the index's last post with the rows after every step. `TestModeratorLabels` checks the words "Approve", "Restore", "Unlock", "Lock", "Delete", "Deleted" and "Waiting for approval" where they show, and their absence for a viewer without the rights.
- The account page (Plan 4, Task 5). `GET /account` shows three forms to a member (a guest still goes to sign in): the password, the email address and the signature. Each shows only when the member holds `account.password`, `account.email` or `account.signature`, and each write checks the right again in `internal/board` (`ChangePassword`, `ChangeEmail`, `SetSignature`; a guest and a member without the right get `ErrForbidden`). **Password:** the page checks the current password through `login.Service.Check` with the member's own username and the address of the request, so the address and name limits, the memory gate and the failure floor of `/login` apply, and a wrong current password counts as a failed sign-in (400 "That is not your current password."; 429 with the limit's text and `Retry-After` when the limits refuse; 503 when the gate is full). The new password needs 10 characters (counted in NFC) and at most 1,024 bytes, must be valid UTF-8, must differ from the username after `account.CleanUsername`, and is typed twice; the cheap checks run before the costly one. The hash of the new password takes a share of the same gate (`login.Service.Hold`), so password changes cannot pile up memory. `password_scheme` becomes `gobb`. The new hash and the delete of every session of the member (`session.DeleteUser`, also for Task 6) commit in one transaction; then one new session starts for the browser with the old session's "remember me" choice, and the answer is 303 with the new cookie. The CSRF token follows the session, so the old token fails and the next page carries the new one. **Email:** needs the current password; an address that another user ID holds (compared with `account.CleanEmail`) is refused with "That address belongs to another account."; the check and the write run under a lock on the address, so two members cannot take one free address together; the member's own address, unchanged, is a no-op (nothing written, `email_verified` stays); a new address sets `email_verified` to false; two members who already share an address are left alone. The address shows on the member's own account page and on no other page. **Signature:** `board.ParseBody` with forum 0 and the stored signature flags, 2,000 bytes at most; an empty text clears it; the form uses `protectPosting` and keeps the text on every error. **Answers:** every password field is empty on every answer, the email field and the signature keep what the member sent, and a success answers 303 to `/account?done=password`, `email` or `signature`, which shows a fixed notice ("Your password is changed.", "Your email address is changed.", "Your signature is saved.") and nothing for any other value. Tests: `TestChangePassword`, `TestChangeEmail`, `TestSetSignature` and `TestSignaturePayloads` (every line of the payload corpus as a signature, each `.signature` element through `htmlcheck.Check`). Carried from the Task 4 review: `TestPostRoutesHaveTokenRows` forces a `tokenRows` row for every POST route (rows added for `/login`, `/logout`, `/scheme`, `/read`, `/f/{id}/read` and the three account routes), the cross-site step of `TestPostingNeedsToken` now sends a good token with a foreign `Origin`, so the origin check refuses on its own, and `TestGuestEditWithGuestTokenChangesNothing` holds that a guest's `POST /p/N/edit` with a good guest token answers 403 or 404 and changes nothing.
- Fixes from the Task 5 review. **A sign-in cannot outlive a password change.** `session.CreateChecked` inserts the session of a sign-in only while the user row still holds the hash that the check verified, the user is active and no user ban that has not ended holds the user; the check and the insert run in one transaction, with `select … for share` on the user row. `login.Service.CheckWith` runs that step as the last part of the check, inside the limits and the floor: a stale account answers like a wrong password, keeps the two limit events and takes the floor (`login.ErrStale` becomes `ErrWrong`), and the check passes the step the hash that is stored after a rehash. `board.ChangePassword` reads the user row `for update` before it writes the hash and deletes the sessions, and the new session after a change is conditional too, so a sign-in in flight, a ban or a reset (Task 6 takes the same lock first) is ordered against it; a ban written straight to `bans` is refused as well. `TestSignInInFlightDoesNotSurviveAPasswordChange` holds a sign-in between the check and the insert, changes the password, and finds exactly one session, the one from the change. **Limits.** Each member gets 5 tries an hour on `/account/email` and 5 on `/account/password`; a try is a POST that reaches the check of the current password, refusals and successes alike. The 6th answers 429 with the form, the kept fields and "Too many tries. Wait an hour and try again."; the count is in memory and starts over with the server. This closes the address oracle and the unlimited hashes of new passwords. The hash of the new password takes its gate share around the hash only (`board.ChangePasswordWith`), and a test fills the gate at that point. **Signatures on new posts.** A new post sets `enable_sig` when the writer is a member who holds `post.signatures` in the forum; an edit keeps the stored flag; a guest post never sets it. **Tests and small fixes.** The lock test reads `pg_locks` for its own database and key; the signature payload test checks the markup after each signature as `postBodies` does after a body; the route tests read the patterns that the running mux recorded and not the source text (a method-less pattern must be named and must refuse a POST); a member who vanishes during the signature form gets the bare form with 404 and no ERROR line; the signature form has no unused notice; an unchanged email address redirects to `/account` with no notice; `TestGuestEditWithGuestTokenChangesNothing` also checks that the refusal is not the "form expired" page.
- A password epoch closes two gaps from the Task 5 re-review (Plan 4, Task 6). Migration `0010_password_epoch.sql` adds `users.password_epoch`, a count of the changes of a member's password. A change on the account page and an administrator's reset each add one, in the same transaction as the hash and the delete of the member's sessions, under `for update` on the user row. `session.CreateChecked` now compares the epoch that the sign-in read with the row, not the hash, so a rehash (the same password stored in the gobb form) is no change: two sign-ins at once on a legacy hash, the double click at cutover, both succeed and give two sessions (before, one of them answered "wrong"). The password change and the email change on the account page hand back the epoch that their check of the current password read; under the row lock they refuse when it is older (`board.ErrPasswordChanged`), answer as a wrong current password, and change nothing. So a member's second browser, or later a reset, cannot be overwritten by a change that checked the old password.
- Admin commands (Plan 4, Task 6). Password reset by email waits until after cutover, so the administrator works from the command line: `gobb user reset-password <name>`, `gobb user ban <name> [-reason TEXT]` and `gobb user unban <name>`. The name matches `username_clean` as at sign-in; an unknown name exits 1 with `no member named <name>` on stderr, a usage error exits 2, and a failure changes nothing, with one exception: when the password of `reset-password` could not be printed, the reset has committed and stays (run the command again for a new password). `reset-password` draws 16 characters from `crypto/rand` over an alphabet of 55 letters and digits without look-alike characters, stores a gobb hash, adds one to the password epoch, ends the member's sessions and prints the password once, as the only line on stdout; the password is never an argument, and it is in no log line, error or `mod_log` row. `ban` adds a `user` ban with no end date and ends the sessions; `-reason` is for staff and is not shown to the member. A ban of a banned member, and an unban of a member with no ban, exit 0 and say so on stderr. Each command that changes something writes one `mod_log` row with a null actor and `{"via":"cli"}`. All three write functions take `for update` on the user row first (`board.ResetPassword`, `BanUser`, `UnbanUser`); `UserByName`, which finds the member before the write starts, takes no lock. So a sign-in in flight, which holds the row `for share` in `session.CreateChecked`, either finishes before the write and loses its session to it, or waits and is refused. Messages go to stderr through `slog`. The README shows the commands through `docker compose exec`; each was run against a Compose stack that CI's override files built, on the example bundle.
- The markup parse is linear in the size of the body (Plan 4, security fix round, A-I1). A body of `*_a` repeated to 60,000 bytes took 1.6 s: goldmark matches emphasis at the end of a paragraph, and each closer walks back over the whole list of delimiters, so a line that interleaves two kinds of delimiter that never pair costs the square of its length. Two budgets, beside the link scan budget, bound it. `delimBudget` (`markup/delimscan.go`) wraps the emphasis parser and the spoiler parser; it starts at 8 steps for each byte of the source plus 1,048,576, and a delimiter spends one step plus, when it can close, the length of the list before it (the longest walk it can cause). Past the budget the rest of the delimiters stay text; the run is read and turned into text, because returning nothing would hand every `*` and `_` to the autolink parser. A paragraph of 1,000 pairs of emphasis fits, and a natural post has tens. The autolink parser had a square of its own: from every space, `*`, `_`, `~` and `(` it reads the run of address characters up to a `@` and the domain after it, so `~a` repeated took 0.2 s. `emailBudget` spends the bytes of that run and domain (64 for each byte of the source plus 262,144); when it is gone, no address is found. Both budgets depend on the input only, never on the clock. Parse times at 60,000 bytes, best of five, before and after: `*_a` 1.62 s and 15 ms; `_*a` 1.62 s and 15 ms; `**_a` 0.91 s and 14 ms; `_**a` 0.92 s and 13 ms; `*a_` 1.61 s and 15 ms; `||*_a` 0.37 s and 12 ms; `~a` 0.20 s and 3 ms; `_a` 0.21 s and 8 ms. `TestParseDelimiterShapesAreLinear` runs these shapes and random mixes at 60,000 and 120,000 bytes (under 50 ms, ten times that under `-race`, and less than three times as long for twice the input); `TestDelimiterBudgetKeepsNaturalPosts`, `TestDelimiterBudgetHoldsALongParagraph` and `TestBoundedParsersChangeNothingBelowTheBudget` (30,000 random sources against the unbounded parsers) show that no natural text changes. The fuzz seeds hold the `*_a` shape.
- Parse before the transaction; a removed title leaves the replies (Plan 4, security fix round, A-I1 ruling 2, B-I1, B-M1). `NewTopic`, `Reply` and `EditPost` parse the body before they take a row lock, so a slow parse holds up no other writer; the rights are those of the `Viewer`, and the writes check the same rules again inside the transaction. A parse error waits for its old place in the transaction, so the order of the answers is unchanged. `EditPost` reads the stored flags and tree of the post, and `edit_count`, without a lock; under the lock on the post it compares `edit_count`, the forum and the flags, and when an edit committed in between it parses again once with the fresh values; a second change gives an `*InputError` on the body that asks the member to try again. `Reply` does the same for the forum of the topic. `TestParseRunsBeforeAnyRowLock` holds each parse open with a test hook and finds no open transaction, no advisory lock and no row lock on the topic, forum, post or member; `TestEditChecksEditCountUnderTheLock` overtakes an edit once and twice. When the title of a topic changes, the same transaction renames every reply whose subject is `Re: ` and the old title, and every moved-topic marker that points at the topic; a reply with another subject stays; the search column follows the subject (`TestRenameRemovesTheOldTitleEverywhere`: a guest's topic page and a search for the old word show nothing). A holder of `mod.lock` who locks a topic that its author locked writes a `topic.lock` row with the moderator as actor, so the author's `topic.lock_own` can no longer undo it; the topic stays locked and only the log row is new, and the topic page offers Lock beside Unlock to the moderator in that case.
- Rate limits on posting; no embeds in signatures (Plan 4, security fix round, A-I1 ruling 3, B-I2, A-D2). A POST to a posting form (new topic, reply, edit; a save or a preview) counts against 60 in 10 minutes for each member and 20 in 10 minutes for each guest address (the `/64` for IPv6, as the sign-in limit counts). Every guest-name check, from a preview or a save, counts against 10 in 10 minutes for each address, and it counts before the name is looked up, so the answer over the limit does not say whether a name belongs to a member. Over a limit the answer is 429 with `Retry-After`, the form, the exact text of the member, and "You are posting too fast. Wait a few minutes and try again." (or the same for name checks). The counts live in memory; `web.Options.MemberPosts`, `GuestPosts` and `NameChecks` set them for a test. A signature renders an embed link as a plain link, with no frame and no provider script (`markup.RenderContext.NoEmbeds`), on the topic page and on the profile, so a signature cannot load third-party script on every page of its member's posts; a post keeps its embeds, and a topic page with a signature embed link and no post embed has the base policy (`TestSignatureEmbedIsAPlainLink`).
- Banned sessions, and the parked account items (Plan 4, security fix round, SB-D1, T6-C4, T6-M1, T6-M2, T6-M6). `session.Lookup` treats a session whose member has a `user` ban that has not ended as no session, so a ban written straight to `bans` makes the next request a guest. The test reads `bans`, so migration `0011_bans_user_index.sql` adds a partial index on `bans (user_id)` for `kind = 'user'`; `TestLookupReadsTheBanIndex` shows the plan. `board.ChangePassword`, `board.ChangeEmail` and `session.Create` are not part of the API any more: the tests reach them through `export_test.go` (and `boardtest.Session`, which goes through `session.CreateChecked`), and `TestNoPublicUncheckedAccountWrites` fails when a non-test file declares either board name again. `ChangePasswordWith` and `ChangeEmailWith` check, under the row lock, that no `user` ban holds the member, as well as the epoch; with a ban they change nothing and return `board.ErrBanned` (an `ErrForbidden`), which the account page answers with 403 and the ban notice. `TestEveryPasswordHashWriterBumpsTheEpoch` scans the SQL of the non-test code and fails when an `UPDATE` sets `password_hash` without adding one to `password_epoch`, except the rehash on sign-in, which it names. The lock test that wrote the other side by hand is now called `TestCreateCheckedWaitsForTheRowLockOfAResetAndOfABan`. `TestConcurrentWritesKeepCountersExact` chooses the posts to delete before its goroutines start.
- The command-line items from the Task 6 review (Plan 4, security fix round, T6-M3, T6-M4, T6-M5). `board.UserByName` also returns the stored username, and every `gobb user` message names the username and the ID of the member the typed name reached (`username=alice user=1`), so a name that the fold matched to another member shows at once (`TestUserCommandsLogTheMatchedMember`). The README and the doc comment of the command say that a reset whose password could not be printed has committed: the old password no longer works, and the administrator runs the command again for a new one. The Task 6 bullet no longer says that `UserByName` takes `for update`; it takes no lock.
- The parse is linear for code spans and for references too (Plan 4, security fix round 2, R2-1, R2-2). The re-review found two more shapes that goldmark parses in more than linear time. A paragraph that opens with runs of 1, 2, ... 200 backticks, each followed by a space, and then holds lines of `a` to 60,000 bytes took 147 ms and allocated 679 MiB in one parse (398 ms at 120,000 bytes): each run finds no closer of its length and reads every later line, one node for each line. `spanBudget` (`markup/spanscan.go`) wraps the code span parser. Before the parser reads, the wrapper looks ahead for the closer, without a node, and spends the bytes it reads plus one for each line from a budget of 16 for each byte of the source plus 262,144. With no closer, or with too few bytes left, the run of backticks stays text, as it does when the parser finds no closer; a span that closes is parsed by goldmark as before. The ladder now takes 5 ms and allocates 15 MiB. The second shape, `[x]:` repeated, took 26 ms at 60,000 bytes and 107 ms at 120,000, and it has two causes. The paragraph transformer moves the lines that remain for each definition, and `refBudget` wraps it: a paragraph spends its lines that start with `[` (the most definitions it can hold) times its lines, from a budget of 16 for each byte plus 1,048,576, and a paragraph that costs more than is left keeps its definitions as text. A paragraph of 800 definitions fits. The link parser reads the text of each `[x]`, `[x][y]` and `![x]` with `Value`, and the `Value` of goldmark finds the line of the text by a walk back from the last line, so `[x]` repeated on 12,000 lines took 40 ms at 60,000 bytes and 145 ms at 120,000. `valueReader` (`markup/linkscan.go`) finds that line by binary search and returns the same bytes, so this part has no budget and changes no tree. `[x]:` repeated now takes 7 ms at 60,000 bytes and 15 ms at 120,000. `TestParseDelimiterShapesAreLinear` runs the ladder, the definitions and the references at 60,000 and 120,000 bytes, `TestHostileShapesAllocateLittle` holds one parse of each to 64 MiB, `TestSpanAndDefinitionBudgetsKeepNaturalPosts` and `...EndAtTheEnd` show what stays markup, and the fuzz seeds hold the shapes. `TestBoundedParsersChangeNothingBelowTheBudget` now compares all four engines (Markdown and Autolink on or off) on text with code spans, links and definitions, and `TestBoundedEnginesWrapTheParsers` reads the budget keys from a parser context of the test, so a wrapper that goes missing fails the test.
- Minor items from the re-review of the security fix round (Plan 4, security fix round 2, R2-3). The scan for `password_hash` writers (`TestEveryPasswordHashWriterBumpsTheEpoch`) joins literals that the code adds with `+` into one statement, and reads `set (password_hash, ...) = (...)` as a write of the hash; `TestEpochScanSeesSplitAndTupleWriters` feeds it both forms, with and without the bump of the epoch, and planting each form in a file of the tree made the test fail (the old scan passed both). A guest who reaches the name-check limit reads the text of the posting limit, "You are posting too fast. Wait a few minutes and try again.", which says nothing about names. Each limiter of the posting forms holds at most 10,000 keys (it was 100,000); the production container has 256 MiB. `TestLookupReadsTheBanIndex` uses `set local` in a transaction, so `enable_seqscan` never reaches the connection that goes back to the pool.
- The definition bound counts a line behind any white space (Plan 4, security fix round 3, R3-1). `refBudget` counted a line as a possible link reference definition only when `[` followed at most 4 spaces. goldmark runs the paragraph transformer before it trims the lines, and its definition parser skips any white space, so a continuation line that starts with a tab or with 5 or more spaces could hold a definition that the count missed, and the quadratic copy ran in full: `[x]: /u` once and then `\t[x]: /u` repeated took 16 ms at 60,000 bytes and 66 ms at 120,000. The count now takes every line of the paragraph whose first byte that is not a space or a tab is `[`; it does not stop at a line without `[`, because a destination or a title may sit on the next line. A paragraph that costs more than the budget keeps its definitions as text, and the budget stays as it was (it fell to 0 before, so every later paragraph of the post lost its definitions too); a refused paragraph costs only the counting loop, so the total stays linear. The five shapes (tab, tab without a destination, tab in a quote with lazy lines, 5 spaces, 8 spaces in a list) are in `hostileShapes`, so `TestParseDelimiterShapesAreLinear` and `TestHostileShapesAllocateLittle` run them, and in the fuzz seeds. `TestRefusedParagraphLeavesTheBudget` puts a paragraph of 2,500 log lines that start with `[` before a short paragraph with a definition and a link, and the link resolves. At 60,000 and 120,000 bytes the shapes now take 5 and 10 ms, 7 and 14 ms, 7 and 13 ms, 3 and 7 ms, and 3 and 6 ms.
- The definition bound counts label ends (Plan 4, security fix round 4, R4-1). The count of lines that start with `[` was not an upper bound on the link reference definitions. When a destination ends its line and a title sits on the next line, goldmark parses the next definition right after that title, in the middle of the line, so `[x]: /u` once and then `"t" [x]: /u` repeated took 12 ms at 60,000 bytes and 52 ms at 120,000, the same as with no bound. `refBudget` now charges a paragraph the number of times `]:` occurs in its lines times its lines. Every definition holds `]:`: goldmark refuses a label unless the byte right after its `]` is `:`, and both bytes sit on one line, because each line ends in a newline. A pasted log of `[12:00] name:` lines holds no `]:`, so it now costs nothing and the definitions near it stay definitions (`TestLogWithoutLabelEndsKeepsItsDefinition`); `TestRefusedParagraphLeavesTheBudget` now uses `[12:00]: name:` lines, which still cost more than the budget. The title shape is in `hostileShapes` and in the fuzz seeds, and now takes 4 ms at 60,000 bytes and 8 ms at 120,000. `TestDefinitionLabelOnTwoLines` shows that a label that spans two lines still resolves.
- Release, backup and upgrade (Plan 4, Task 7). A tag `vX.Y.Z` runs `.github/workflows/release.yml` on the self-hosted runner. Four jobs run in order, and each needs the one before. `releasecheck` runs `go run ./scripts/releasecheck <tag>`, which refuses a tag that is not `vX.Y.Z`, a first changelog section that is not exactly `X.Y.Z (YYYY-MM-DD)` (so it refuses `(unreleased)`), an `internal/version` that differs from the tag, and an empty section. `build` makes the binaries for `linux/amd64`, `linux/arm64` and `darwin/arm64` with `CGO_ENABLED=0 -trimpath`, each with a SHA-256 sum, builds the image for both platforms, and runs the amd64 image. `push` pushes `ghcr.io/evanwtf/gobb:X.Y.Z` and `:latest` for both platforms and runs the pulled image. `release` creates the GitHub release last, with the changelog section as its notes: the notes never come from the message of the tag. A manual run has the input `dry_run`, true by default. It runs `releasecheck -dry-run` (the version comes from `internal/version`, and `(unreleased)` is accepted; every other check runs), builds everything and runs the local image, and its `push` and `release` jobs do not run. A manual run with `dry_run` false refuses unless the ref is a `vX.Y.Z` tag. The runner is shared, so the workflow installs no emulation: the build stage of the `Dockerfile` runs on `$BUILDPLATFORM` and cross-compiles with `TARGETOS` and `TARGETARCH`, and the runtime stage has no `RUN` step. Registry credentials live in a Docker config folder under the run's temp folder, which the last step of the job removes, and the job removes its buildx builder.
- The backup round trip (Plan 4, Task 7). The README gains "Back up and upgrade": `pg_dump -Fc` through `docker compose exec -T postgres`, a `tar` of the data volume, `docker compose pull && docker compose up -d --wait`, and the restore with `pg_restore --clean --if-exists` and the tar file. It says to keep the files on the server and to check the dump's size before the upgrade. The `image` job of CI runs the dump, restores it into a new database, and runs `scripts/rowcounts`, which compares the row count of every table that `db.DataTables` lists. A table that a later migration adds is covered with no CI edit. The command fails on a different table list, a different count, or a source with no rows. `compose.ci.yaml` publishes the `postgres` port on localhost, at a random host port, so the count can run on the runner.
- PostgreSQL 18 (owner, 2026-10-24). The Compose stack and the PostgreSQL service of CI use the official `postgres:18` image (18.6, the newest release), in place of `postgres:16`. The 18 image keeps its data in `/var/lib/postgresql/18/docker` and declares `/var/lib/postgresql` as its volume, so `compose.yaml` mounts `gobb-pg` at `/var/lib/postgresql`, not at `.../data`. A volume that a 16 stack made holds its cluster in `data`, so the 18 stack ignores it and starts an empty cluster in its own folder. To move a board from a 16 stack, follow "Back up and upgrade" in the README: dump the database and archive the data volume on 16, then start the 18 stack and restore both. Never run `docker compose down -v` for this: it also deletes the data volume. To discard only the old database volume, remove `gobb_gobb-pg` (`docker volume rm`). gobb still supports PostgreSQL 16 or later. The README recipes that start a helper container use `postgres:18`.
- Fixes from the Task 7 review (Plan 4, Task 7 fix round 1). **The restore drops the database.** `pg_restore --clean` drops only the objects that are in the dump, so a table that a newer migration added would stay, and the restored `schema_migrations` would make the next upgrade create it again. The README restore now runs `drop database gobb with (force)` and `create database gobb` on the `postgres` database, then `pg_restore` into the empty database. **The restore checks before it deletes.** It is one `&&` chain: `STAMP` is not empty, the dump and the archive for that stamp both exist and are not empty, `pg_restore --list` reads the dump, and `tar tzf` reads the archive. Only then does it stop gobb, drop the database and empty the data volume. The "newest" picker chooses only a stamp that has both files, and a failed dump or archive is deleted. CI runs the README's own commands (`scripts/readme-block.sh` takes each block from the file): it restores over a database that holds an extra table with a foreign key to `users`, and checks that the table is gone, that every row count matches, and that gobb starts on the restored board. It also runs the restore with the archive missing, an empty `STAMP`, a dump that `pg_restore` cannot read and an archive that `tar` cannot read, and checks that each run fails and deletes nothing. `scripts/rowcounts` also counts `schema_migrations`. The README says that `GOBB_VERSION` is the version without the `v`, such as `0.1.0`.
- Guards on the release (Plan 4, Task 7 fix round 1). In a tag run, `releasecheck` also refuses a tag whose commit is not on `main`, and a tag that already has a release (a re-run of a published tag would push the images again). The script refuses release notes over 125,000 characters, the limit of GitHub, before any image is public; its tests cover CRLF line ends and the limit. Every job that uses Docker sets `DOCKER_CONFIG` in its first step, and its clean-up step touches Docker only when that folder is the one of the run. The workflow runs its steps with `bash -e -o pipefail`. The image carries the label `org.opencontainers.image.source`, which links the package to the repo, and the README says to make the package public after the first release.
- The build line in the footer (Plan 4, Task 8; owner, 2026-10-25). Every page ends with one line in small type (class `build-line`). A 2xx answer to a GET or HEAD request shows `Rendered in 0.02 s · gobb 0.1.0 · build 0f0f0fac`; a viewer who holds the board-wide `admin` right also sees the count: `Rendered in 0.02 s with 5 queries · gobb 0.1.0 · build 0f0f0fac`. Every answer to a POST and every answer that is not 2xx shows `gobb 0.1.0 · build 0f0f0fac`, so the 404 of a hidden topic and the 404 of a missing one stay byte-identical. The time runs from the moment the request enters the handler chain until just before the last run of the page template, rounded up to the next 10 ms (never `0.00 s`). The count comes from `db.CountTracer`, a pgx tracer on every pool that `db.Open` and `dbtest` build: it counts each exec, query, batch item and copy, and a transaction's begin and commit, into a counter in the request's context (`db.WithCounter`), so concurrent requests never mix their counts, and a statement with no counter (a background job, start-up) counts nowhere and does not fail. The build is `internal/version.Build()`: the first 8 hex characters of `version.Commit`, which the linker sets (`-X`), or of the `vcs.revision` that the Go tool stamps, or `unknown`; any value that is not 7 to 40 hex characters shows as `unknown`, so a build argument cannot put markup into a page. The `Dockerfile` takes `ARG GOBB_COMMIT` in the build stage only (the runtime stage still has no `RUN`), `compose.yaml` passes it on, the CI `image` job passes `github.sha` and checks that the served footer of `/`, `/t/1` and a 404 names its first 8 characters, and the release workflow passes `github.sha` to the binaries and to all four image builds and checks that every binary holds it. The line holds no member, host, address, memory figure or per-query timing, adds no `Server-Timing` header, and needs no inline style or script. Tests: `TestFooterShowsBuildAndTiming`, `TestFooterHasNoTimingOnErrors`, `TestQueryCountIsPerRequest`, `TestRenderTimeRoundsUp`, `TestBuildCommitIsSanitized`, and the counter tests in `internal/db`. Fix round 1 (C1): the count was exact, and on the 200 page of a failed sign-in it told a stranger whether a username exists (4 queries for a known name, 5 for an unknown one). Only an admin sees the count now, and a POST answer never shows a time. The Dockerfile refuses a `GOBB_COMMIT` that is not hex, so a build argument cannot add linker flags, and the release workflow builds the binaries with `-buildvcs=false`, so its check of the SHA in each binary proves `-X`. Further tests: `TestFooterShowsCountOnlyToAdmins`, `TestFooterHidesWhetherANameExists`, `TestFooterIsPlainOnPost`, `TestQuoteOfHiddenPostLooksMissing`.
- The edit reason belongs to the last editor (Plan 4, final fix, FF-1). `EditPost` kept the old `edit_reason` when an edit gave none, so after a moderator edited a post with a reason and the author edited it again, the page read "Edited by <author> ... Reason: <moderator's reason>". An imported reason behaved the same way. An edit now writes its own reason, and an edit that gives none clears it. Tests: `TestEditReasonBelongsToLastEditor` (board: an imported reason, then a moderator's, then the author's edit) and `TestEditNoteReasonFollowsLastEditor` (the edit note on the topic page).
- A repeated delete is a success; a ban drops the member's addresses (Plan 4, final fix, FF-2, FF-3). A second click on "Delete" in the confirm page used to end on a 404, because the post was already gone for its author. `POST /p/{id}/delete` with `confirm=1` now answers the same 303, to the same target, when the viewer is the author of the post and the post is deleted (`board.DeletedOwnPost`; the author already knows that the post exists, spec 14). Another member, a guest, a post that does not exist and a deleted post of another author still answer 404, and the confirm step without `confirm=1` is unchanged. The password double submit is unchanged. `BanUser` also deletes the member's `user_ips` rows in the ban transaction (spec 10), so an unban does not bring back the trust of the old addresses; a refused ban (`ErrAlreadyBanned`) removes nothing, and the README says so under `user ban`. Tests: `TestRepeatedDeleteConfirmIsASuccess`, `TestBanUserDropsTrustedAddresses`.
- A `darwin/amd64` binary, and README fixes for v0.1.0 (Plan 4, final fix, FF-4, FF-5, FF-6, FF-8). The release workflow builds `darwin/amd64` with the other three (`CGO_ENABLED=0 -trimpath -buildvcs=false` and the commit through `-X`), checks its build info and its commit like the others, and writes its own SHA-256 sum; the README "Release" section lists four binaries. The README Status says that members post, edit and moderate, and that private messages, uploads and email come later. The build-line text says that guests and members see the time, that an admin also sees the query count, and that a POST answer and an error page show only the version and the build. **The restore says why it stopped.** The README "Restore" section starts with `export BACKUP_DIR="$HOME/gobb-backups"` (outside every block, so CI does not run it and a check in CI reads it), and each check in the restore chain prints its own message on stderr when it fails. The chain still stops, still exits non-zero and still deletes nothing. The CI round trip now asserts the message of each of its four refusal cases. The Task 0 bullet above names three new tests, not two.
- The soundness property of the definition bound is a test (Plan 4, final fix, FF-7). The budget for link reference definitions charges a paragraph for its `]:` count, and that is sound only while goldmark never takes more definitions from a paragraph than that count. The argument rests on facts of goldmark v1.8.6, so `markup/refbound_test.go` states it as a property: it wraps goldmark's own transformer in a recorder, parses 20,000 random container-heavy sources from a fixed seed (quotes, lists, lazy lines, titles, labels over two lines, `\]`, `]\n:`), and compares, for each paragraph, the definitions that goldmark took with the `]:` count of the paragraph's segments. A second test pins the recorder on sources with known answers, and the property test fails when the generator is too weak to reach the bound. With the bound of round 3 (lines that start with `[`) in place of the `]:` count, the property test fails. `go.mod` carries a note beside the goldmark line: read `markup/spanscan.go` and run this test before an upgrade. Tests: `TestRecorderCountsDefinitions`, `TestGoldmarkTakesNoMoreDefinitionsThanLabelEnds`.
