# gobb Plan 4b: Settings and private messages — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Members of the first board keep what they use on cutover day. They set their time zone, date format, avatar, "About you" and birthday on an account page that saves each change as it happens. They read, send, reply to, quote and delete private messages, the imported ones included.

**Architecture:**
- **Bundle and import.** The bundle format moves to version 2. It gains the new member fields and drops the files that gobb has no page for. `bundle.Check` accepts only time zones that are places.
- **Board functions.** `internal/board` gains one write function for each setting and for each message action. Each one checks the rights itself.
- **Pages.** `internal/web` adds the forms and the message pages. Each POST handler calls one board function. Without script, the handler answers 303. When `gobb.js` sends the change, it answers 204 or a short JSON error.
- **Avatars.** A new `internal/avatar` package decodes an upload under the memory gate and writes a fresh 128 × 128 PNG.
- **Times.** One `clock` value per page carries the viewer's zone, the viewer's date format and the time of the request. Every template prints times through it.

**Tech Stack:** Go 1.27 standard library (`image/png`, `image/jpeg`, `image/gif` for avatars); pgx v5; the IANA tz database's `zone.tab`; `golang.org/x/net/html` in tests only, as in Plan 4.

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md`, sections 7 ("Accounts"), 8, 10, 12, 13 and 17 ("Release 1": the settings, "settings save when they change", private messages, and "Every imported setting is one the member can change"). Issues #9 and #10. Roadmap row 4b.

**Workers:** Sonnet 5.5, high effort; the controller sets the model and the effort on every dispatch. Reviewer: Opus, high effort, after every task. After Task 9, a separate Opus security review covers Tasks 1–9. An Opus review of the whole plan comes at the end.

**Preview:** the owner approved the pages in a static preview on 2026-10-25. The controller keeps it, with made-up data only. Each page task names the preview page that it matches.

## Global Constraints

- **Clean room.** Work only from this repo and public standards: RFCs, the Go and PostgreSQL docs, the WHATWG HTML and Fetch standards, the IANA tz database, and the PNG, JPEG and GIF specifications. Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. Name no other forum software in code, docs, tests or commits. (`AGENTS.md`; spec 1.)
- **Real data stays with the controller.** Workers use `bundletest` fixtures and made-up data only. The controller runs each checkpoint against its private copy of the first board.
- **This repo is public.** Never commit hostnames, IP addresses other than `127.0.0.1`, `10.x` and `2001:db8::/32`, home-directory paths, secret values or forum data.
- **No new dependency.** The avatar resize is written here. Nothing in `go.mod` changes.
- **Commits.**
  - Subjects are `area: imperative summary`.
  - No agent session URLs or IDs, and no agent trailers.
  - Work lands on `main`. Push after every task.
  - Each task adds its bullet to `docs/changelog.md` under a new `## 0.2.0 (unreleased)` section.
- **PostgreSQL in tests.** Tests that need it use `dbtest.New` (through `boardtest.Load`) and skip when `GOBB_TEST_DATABASE_URL` is unset. CI sets it and fails on any skip.
- **Migrations.**
  - Never edit a migration that is already on `main`.
  - This plan adds `0012_settings_messages.sql` (Task 2) and nothing else.
- **One write path for member text (owner: security).**
  - A post, an edit, a signature, a private message and a preview each go through `board.ParseBodyKeeping`, the function under `board.ParseBody`.
  - Task 8 adds `board.ParseMessage`, a thin entry into the same function.
  - Nothing stores or echoes HTML from a request.
  - "About you" fields are plain text. Templates print them escaped, and no parser runs on them.
- **Rights are checked in `internal/board`, not only in `internal/web`.**
  - A board write returns `ErrForbidden` or `ErrNotFound` on its own. Hiding a button is never the check.
  - A message that the viewer is not a party to gets `ErrNotFound`, the same as a missing one (spec 14).
- **GET and HEAD change nothing**, with one exception. Opening a message marks it read, as opening a topic writes a topic mark (Plan 3, `renderThen`).
- **Every change is a POST through the guard** (Cross-Origin Protection and the CSRF token).
  - Without script, a successful POST answers 303 to a GET page. A refused one answers 400 with the form, the values the member sent, and a message beside the field.
  - When `gobb.js` sends a setting, the answer is 204, or JSON (Task 6).
- **Pages behind sign-in.**
  - A guest who asks for `/account` or any `/messages` page gets 302 to `/login`, and comes back afterwards.
  - A POST from a guest gets 403.
  - These pages carry `noindex` and `Cache-Control: private, no-store`.
- **No inline script, no `style` attributes and no `<style>` elements** in templates. All script lives in `internal/web/static/gobb.js`. New pages keep the CSP of Plan 4, with `connect-src 'self'` for the saves.
- **Limits.**

  | Field | Limit |
  |---|---|
  | Location, occupation | 0–100 characters after trimming |
  | Interests | 0–200 characters after trimming |
  | Website | 0–200 bytes |
  | Message subject | 1–120 characters after trimming |
  | Message body | 1–60,000 bytes |
  | Recipients | 1–10 names; more than one needs `pm.send_many` |
  | Avatar upload | at most 1 MiB (1,048,576 bytes); at most 8,000 pixels a side and 16,000,000 pixels in all |
  | Birthday year | 1900 to the current year minus 19 (2007 in 2026) |

- **Rate limits.** Each count is per member, in memory, in a sliding window, as in Plan 4.
  - Setting saves: 120 in 10 minutes.
  - Avatar uploads: 10 in an hour.
  - Message sends: 10 in 10 minutes.
  - A preview or a send of a message also counts against Plan 4's posting limit, because it costs a parse.
- **Checkpoints.** Each task ends with a `Checkpoint:` line. The controller runs it on its private copy before the next task starts.

## Review Focus

1. **An auto-save of one field wipes the others.** The script sends only the field that changed. A handler that reads every field of its section would clear the location when the member changes the website. Test: `TestAutoSaveIsPartial` (Task 6).
2. **An avatar that is a decompression bomb or a polyglot.** A 30 KB PNG can declare 50,000 × 50,000 pixels. A GIF can also be valid HTML. The first must be refused before it is decoded, and the second must be stored only as a fresh PNG. Tests: `TestMakeRefusesHugeBeforeDecode` and `TestMakeOutputIsFresh` (Task 5).
3. **Another member's message, by changing the ID.** `/messages/{id}`, its reply form, its quote and its delete must answer 404 to a member who is not a party. The body must hold none of the message's text. Tests: `TestMessageNotFoundForOthers` (Task 8) and `TestMessagePagesNotFound` (Task 9).
4. **A blind-copy recipient shows to the others.** Only the author and the blind-copy recipient may see a `bcc` recipient. Test: `TestBccHidden` (Task 8).
5. **A stored zone that the list no longer holds.** A tz database update can rename a zone. The account page must still show the member's zone as selected, and saving another setting must not fail on it. Test: `TestAccountPageUnknownStoredZone` (Task 6).

---

## Decisions this plan fixes

**URLs.** All are under the base path. The URLs of Plans 3 and 4 stay.

| Path | What |
|---|---|
| `POST /account/timezone` | the time zone (`timezone`) |
| `POST /account/dateformat` | the date format (`dateformat`: `standard`, `relative` or `iso`) |
| `POST /account/about` | any of `location`, `occupation`, `interests` and `website`, and the birthday (`bday`, `bmonth`, `byear`, sent together) |
| `POST /account/avatar` | an upload (`multipart/form-data`, field `avatar`) |
| `POST /account/avatar/remove` | remove the avatar |
| `GET /messages`, `GET /messages/sent` | the inbox and the sent list, 25 a page, newest first, `?page=N` |
| `GET /messages/new?to={name}` | the compose form; `to` fills the To field |
| `POST /messages/new` | send, or preview with `preview` in the form |
| `GET /messages/{id}` | one message; marks it read for the viewer |
| `GET /messages/{id}/reply`, `?quote=1` | the compose form, filled for a reply or a quote |
| `POST /messages/{id}/delete` | delete from the viewer's own view, with a confirm step like posts |

**Who may do what.** All the rights are board-wide.

| Action | Needs |
|---|---|
| Time zone, date format | a member |
| "About you", birthday | `account.profile` |
| Avatar upload or remove | `account.avatar` |
| Inbox, sent list, read a message | `pm.read`, and for a message: the viewer is its author (not `author_deleted`) or a recipient (not `is_deleted`) |
| Send, reply, quote | `pm.send`; `pm.send_many` for more than one recipient; `pm.markdown`, `pm.smilies` and `pm.images` set the flags and the image rule |
| Delete from own view | `pm.delete`, and the viewer is a party |

**Date formats.** Guests see `standard`. The `<time>` element keeps `datetime` in UTC (Plan 3).

| Format | A time | A date |
|---|---|---|
| `standard` | `Oct 25, 2026, 5:07 pm` | `Oct 25, 2026` |
| `relative` | the relative text below; `title` holds the `standard` time | the same relative text; `title` holds the `standard` date |
| `iso` | `2026-10-25T17:07:00-04:00` (layout `2006-01-02T15:04:05-07:00`, so UTC prints `+00:00`) | `2026-10-25` |

**Relative text.** The age `d` is `now − t`.

| `d` | Text |
|---|---|
| `t` is up to 60 s ahead, or `d` < 10 s | `just now` |
| `t` is more than 60 s ahead | the `standard` text |
| 10 s ≤ `d` < 2 min | `N seconds ago` |
| 2 min ≤ `d` < 2 h | `N minutes ago` |
| 2 h ≤ `d` < 48 h | `N hours ago` |
| 48 h ≤ `d` < 180 days | `N days ago` (24-hour days) |
| 180 days ≤ `d`, and fewer than 24 whole calendar months in the viewer's zone | `N months ago` (whole calendar months) |
| otherwise | `N years ago` (whole calendar years) |

N is never 1, so no singular form is needed.

**Time zones.**
- The picker lists `tz.Zones`: every zone of IANA `zone.tab`, by name.
- Above them, an optgroup "Common" holds `tz.Common` with labels: Eastern: New York; Central: Chicago; Mountain: Denver; Mountain, no DST: Phoenix; Pacific: Los Angeles; Alaska: Anchorage; Hawaii: Honolulu; Atlantic: Halifax; London; Central Europe: Berlin.
- The page offers no fixed offset and no bare UTC (owner, 2026-10-25).

**Avatars.**
- An upload is PNG, JPEG or GIF, by its first bytes. The name and the content type that the browser sends do not count.
- gobb decodes the first frame, crops the center square, and resamples it to 128 × 128. A shrink uses an area average and a growth uses the nearest pixel.
- gobb stores a new PNG at `avatars/u{id}-{16 random hex}.png`, so a cached copy of the old avatar never shows.
- Once the change commits, gobb deletes the old file.

**Auto-save (spec 17, "settings save when they change").**
- **Where it applies.** A form with `data-autosave` saves each field on its `change` event. The `change` event of a text box fires when the member leaves it. Each field has a status line, `<p class="save-status" id="{field}-status" role="status" aria-live="polite">`.
- **What the script sends.** It sends the field, plus `csrf`, as `application/x-www-form-urlencoded`. The birthday's three lists go together. An avatar goes as `multipart/form-data`. Each request carries the header `X-Gobb-Save: 1`.
- **Order.** One save is in flight for each field at a time. A change made during a save waits, and only its latest value is sent.
- **Leaving the page.** On `pagehide`, the script sends any changed text box that still has focus. A text save uses `keepalive: true`. An avatar does not: the Fetch standard caps a keepalive body at 64 KiB.
- **Answers with `X-Gobb-Save: 1`.**
  - Success: 204. An avatar upload answers 200 with `{"src":"{site path of the new file}"}`, and a remove answers 204.
  - Refused: 400 with `{"field":"website","message":"…"}`.
  - Other failures: 403, 404, 413, 429 or 503, with `{"message":"…"}`.
  - Each JSON answer is `application/json` with `nosniff`.
- **Status text.** "Saving…", then "Saved.", or the message. A refused field keeps its value and gets `aria-invalid="true"`.
- **Without script.** Each section shows its Save button (`class="save-fallback"`; `.js .save-fallback { display: none }`), and the answer is 303 to `/account?done={section}`.
- **Password and email.** These two forms keep their button and are not `data-autosave`.

**Header (preview: every page).**
- "Signed in as {name}" and "Sign out" move into the header row, at the right of the board name. The name links to `/account`. A guest's "Sign in" link moves there too.
- The nav keeps Index, "New since your last visit", Members, Team, the search box and Advanced. It adds "Messages" after Team, for a member with `pm.read`.
- When unread messages exist, "Messages" carries a `badge-new` pill, "N new".

**Bundle format version 2.**
- **Changes to `users.jsonl`:**
  - It drops `notify_on_post`. The column keeps its default, because email comes later with its own switch.
  - It adds `date_format`, `location`, `occupation`, `interests` and `website`.
  - It adds `birth_day`?, `birth_month`? and `birth_year`?.
- **Files.** The format drops `topic_watches.jsonl` and `bookmarks.jsonl`, because gobb has no page for them (spec 17). It now has 21 files. The tables stay in the schema.
- **Time zones.** `settings.timezone` and `users.timezone` must be in `tz.Zones`.
- **Version 1.** A version 1 bundle is refused with: `bundle format version 1; this gobb reads version 2. Build the bundle again with a converter for version 2.`

---

### Task 1: Bundle format version 2 and the zone list

**Files:**
- Create: `internal/tz/zones.go` (generated), `internal/tz/tz.go`, `internal/tz/tz_test.go`, `scripts/zonetab/main.go`
- Modify: `bundle/types.go`, `bundle/fields.go`, `bundle/check.go`, `bundle/reader.go` and their tests
- Modify: `docs/bundle.md`, `docs/example-bundle/`, `internal/bundletest/bundletest.go`
- Modify: `docs/changelog.md` (open `## 0.2.0 (unreleased)`)

**Interfaces:**
- Produces:
  - `tz.Zones []string`: sorted, from `zone.tab`.
  - `tz.Common []tz.Choice` with `tz.Choice{Name, Label string}`, in the order of "Decisions".
  - `tz.Known(name string) bool`.
  - `bundle.KnownZone(name string) bool`, which calls `tz.Known`. A converter outside this module cannot import `internal/tz`, so it maps zones with this function.
  - `bundle.Version = 2`.
  - `bundle.User` gains these fields:
    - `DateFormat string` (`json:"date_format"`);
    - `Location, Occupation, Interests, Website string`;
    - `BirthDay, BirthMonth, BirthYear *int` (`birth_day`, `birth_month`, `birth_year`).
  - `bundle.User` loses `NotifyOnPost`. `bundle.TopicWatch` and `bundle.Bookmark` are removed.
- Fixtures:
  - alice: location "Lake Placid, NY", website `https://example.com/alice`, birthday 14 March 1984.
  - bob: birthday 2 July, no year.
  - carol: date format `relative`.
  - Everyone else: `standard` and empty fields.

Rules for `users.jsonl`:
- `date_format` is `standard`, `relative` or `iso`.
- `location` and `occupation` hold at most 100 characters, and `interests` at most 200. None holds a control character or a line break.
- `website` is empty, or it meets all of these:
  - it is at most 200 bytes;
  - its scheme is `http` or `https`, in any case;
  - it has a host;
  - it has no user information, no white space and no control character.
- **Birthday.**
  - `birth_day` and `birth_month` are both null or both set. `birth_year` may be set only when both are.
  - The date exists: 29 February needs a null year or a leap year.
  - The year is 1900 to the year of `manifest.created_at` minus 19.

- [ ] **Step 1: Generate the zone list**
  - Write `scripts/zonetab`. It reads a `zone.tab` path and writes `internal/tz/zones.go`, with a header that names the tzdata version.
  - Use the `zone.tab` of the IANA release whose version matches `DATA` in `$(go env GOROOT)/lib/time/update.bash`.
- [ ] **Step 2: Write the failing tests**
  - `TestZonesLoad`: every name in `tz.Zones` and `tz.Common` loads with `time.LoadLocation`. `tz.Zones` holds 400 to 450 names, sorted, with no duplicate.
  - `TestKnown`:
    - true for `America/New_York`, `Europe/Kyiv` and `Pacific/Funafuti`;
    - false for `Etc/GMT+4`, `UTC`, `Etc/UTC`, `GMT`, `EST5EDT`, `Local`, `Australia/ACT` and "".
  - `TestUserZoneMustBePlace`: a user with `Etc/GMT+4` fails `bundle.Check` on `users.jsonl`, field `timezone`. So do settings with `UTC`.
  - `TestUserVersion2Fields`, a table, one case for each rule above:
    - `date_format` "custom" is refused;
    - `website` `javascript:alert(1)`, `ftp://x.org`, `example.com`, `https://` and `https://a:b@x.org` are refused, and `HTTPS://Example.com/a` passes;
    - 201 characters of interests are refused;
    - a location with a line break is refused;
    - a day with no month is refused;
    - a year with no day is refused;
    - 29 February 2001 is refused;
    - 29 February with no year passes;
    - with `created_at` in 2026, the year 2008 is refused and 2007 passes.
  - `TestVersion1Refused`: a manifest with `"version": 1` gives the message in "Decisions".
  - `TestWatchFilesUnknown`: a bundle directory that holds `topic_watches.jsonl` or `bookmarks.jsonl` is refused as an unknown entry.
  - The existing test that checks `docs/example-bundle/` against the format keeps passing after the update.
- [ ] **Step 3: Run them to verify they fail.** Run: `go test ./internal/tz/ ./bundle/`. Expected: FAIL.
- [ ] **Step 4: Implement.**
  - Write the format changes.
  - Update `docs/bundle.md` to "format version 2", and add a section "Changes from version 1".
  - Update `docs/example-bundle/` and the fixtures.
  - Keep `go build ./...` green in this commit. In `internal/importer`:
    - remove `loadTopicWatches`, `loadBookmarks` and their rows of the load order;
    - drop `notify_on_post` from the insert of `loadUsers`;
    - update the importer tests that count those files.

    The new columns come in Task 2.
- [ ] **Step 5: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS, no skips.
- [ ] **Step 6: Commit and push.** `git commit -m "bundle: format version 2, with member settings and place-only time zones"`

**Checkpoint:** none. The private converter moves to version 2 in its own plan.

---

### Task 2: Migration and import

**Files:**
- Create: `internal/db/migrations/0012_settings_messages.sql`
- Modify: `internal/importer/people.go`, `internal/importer/content.go`, `internal/importer/importer.go` and their tests, `docs/changelog.md`

**Interfaces:**
- Consumes: Task 1's `bundle.User`.
- Produces: these columns of `users`, which every later task reads:
  - `date_format text not null default 'standard'`, checked against the three values;
  - `location`, `occupation`, `interests` and `website`, each `text not null default ''`, with `char_length` checks of 100, 100, 200 and 200;
  - `birth_day`, `birth_month` and `birth_year`, each `smallint null`. A check holds the pairing rules of Task 1. The date and the age rules live in Go.
- Indexes:
  - `users (birth_month, birth_day) where birth_month is not null`;
  - `pm_recipients (user_id, pm_id desc) where not is_deleted`;
  - `pms (author_id, id desc) where not author_deleted`.

- [ ] **Step 1: Write the failing tests**
  - `TestImportVersion2Fields`: after importing `bundletest.Small()`:
    - alice's location, website and birthday read back;
    - carol's `date_format` is `relative`;
    - bob's `birth_year` is null.
  - `TestImportNoWatches`: after the import, `topic_watches` and `bookmarks` hold no row.
  - `TestMigrationChecks`: an update that sets `birth_year` with a null `birth_day` fails, and so does `date_format = 'x'`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.** Write the new columns in `loadUsers`.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "importer: member settings, and no watches or bookmarks"`

**Checkpoint:** the controller applies the migration to its private copy, and `gobb serve` starts on it.

---

### Task 3: Times in the viewer's format

**Files:**
- Create: `internal/web/clock.go`, `internal/web/clock_test.go`
- Modify: `internal/board/board.go` (`Viewer.DateFormat`, read in `Member`; `standard` in `Guest`), `internal/web/pages.go` (the `time` and `date` funcs), `internal/web/site.go` (`page.Clock`)
- Modify: `internal/web/forum.go`, `internal/web/new.go`, `internal/web/post.go`, `internal/web/topic.go`
- Modify: every template that calls `time` or `date`, and `docs/changelog.md`

**Interfaces:**
- Produces:
  - `type clock struct { Loc *time.Location; Format string; Now time.Time }`. `begin` sets `page.Clock` from the viewer and `s.opt.Now()`.
  - `timeTag(t time.Time, c clock, dateOnly bool) template.HTML`.
  - `relText(t, now time.Time, loc *time.Location) string`.
  - The template calls become `{{time .At $.Clock}}` and `{{date .Joined $.Clock}}`. The `Loc` fields of `forumRows`, `topicRowView` and `postView` become `Clock clock`.

- [ ] **Step 1: Write the failing tests**
  - `TestRelativeBands`: with `now` = 2026-10-25 17:07:00 in `America/New_York`, a table of ages:
    - 9 s gives `just now`; 10 s gives `10 seconds ago`; 92 s gives `92 seconds ago`; 119 s gives `119 seconds ago`;
    - 120 s gives `2 minutes ago`; 98 min gives `98 minutes ago`;
    - 2 h gives `2 hours ago`; 39 h gives `39 hours ago`;
    - 48 h gives `2 days ago`; 96 days gives `96 days ago`; 179 days gives `179 days ago`;
    - `t` = 2025-08-20 gives `14 months ago`;
    - `t` = 2023-09-01 gives `3 years ago`;
    - `t` 30 s ahead gives `just now`;
    - `t` 5 min ahead gives the `standard` text.
  - `TestRelativeAcrossDST`: in `America/New_York`, `t` = 2026-10-31 20:00 and `now` = 2026-11-01 20:00 give `25 hours ago`. The count uses elapsed time, not wall clocks: daylight saving time ends between them.
  - `TestTimeTagFormats`:
    - `standard` gives `Oct 25, 2026, 5:07 pm`;
    - `iso` gives `2026-10-25T17:07:00-04:00`, and in `Europe/London` in January it gives `+00:00`;
    - `relative` gives the relative text with a `title` of the `standard` text;
    - the `date` forms give `Oct 25, 2026` and `2026-10-25`;
    - every form keeps `datetime` in UTC.
  - `TestPagesUseViewerFormat`: on topic 1, alice (`standard`) sees `May 2, 2019`, and carol (`relative`) sees `years ago`. A guest sees `standard`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips. Every existing template test still passes.
- [ ] **Step 5: Commit and push.** `git commit -m "web: show times in the viewer's date format"`

**Checkpoint:** on the private copy, the controller sets its made-up member to `relative`, then to `iso`, by SQL. It checks the index, a forum, a topic and a profile.

---

### Task 4: The header and the form styles

**Files:**
- Modify: `internal/web/templates/layout.html`, `internal/web/site.go` (`navLink.Badge string`), `internal/web/static/gobb.css`, `internal/web/layout_test.go` (or the test file that checks the layout), `docs/changelog.md`

**Interfaces:**
- Produces:
  - `navLink.Badge`, which Task 9 fills for "Messages".
  - These CSS classes: `.header-user`, `.save-status`, `.save-fallback`, `.birthday-row`, `.avatar-img`, and the rules for `fieldset.field` and `legend`.

Rules (preview: every page, and `account.html`):
- `.header-inner` becomes a wrapping flex row: the brand at the left, `.header-user` at the right.
- `.header-user` holds "Signed in as <a href="/account">{name}</a>" and the Sign out form, or a guest's "Sign in" link.
- On `/account`, the name link carries `aria-current="page"`.
- A `fieldset.field` has no border and no padding. Its `legend` looks like a field label.
- `.field-check` labels inside `.account-section` are regular weight. Only the sample values are bold.

- [ ] **Step 1: Write the failing tests**
  - `TestHeaderUser`:
    - a member's page holds `.header-user` with a link to `/account` whose text is the name;
    - a guest's page holds the "Sign in" link there;
    - the nav holds no "Signed in as".
  - `TestCSPStrictEverywhere` still passes, and the layout has no `style` attribute.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: the member's links move to the header"`

**Checkpoint:**
- The controller takes headless screenshots of the index at 1280 and 400 pixels wide, signed in and as a guest.
- At 1280, the nav is one row.
- The owner looks at them.

---

### Task 5: Settings in the board, and the avatar maker

**Files:**
- Create: `internal/board/settings.go`, `internal/board/settings_test.go`, `internal/avatar/avatar.go`, `internal/avatar/avatar_test.go`
- Modify: `docs/changelog.md`

**Interfaces:**
- Consumes: `tz.Known`, `mayAccount`, `inTx`, `InputError`.
- Produces:
  - `board.SetTimezone(ctx, pool, v Viewer, zone string) error`. Refusal: `InputError{Field: "timezone", Message: "Pick a time zone from the list."}`.
  - `board.SetDateFormat(ctx, pool, v Viewer, format string) error`. Refusal: field `dateformat`, "Pick one of the three formats."
  - `board.AboutChange{Location, Occupation, Interests, Website *string}` and `board.SetAbout(ctx, pool, v Viewer, c AboutChange) error`. A nil field stays as it is. Each set field is trimmed and checked by Task 1's rules.
    - Too long: "Use at most 100 characters." (or 200).
    - Control character: "Use one line of plain text."
    - Website: "Enter an address that starts with https:// or http://."
  - `board.Birthday{Day, Month, Year int}` (0 means unset) and `board.SetBirthday(ctx, pool, v Viewer, b Birthday, now time.Time) error`. The refusals, on field `birthday`:
    - "Pick both a day and a month, or neither."
    - "A year needs a day and a month."
    - "{Month} has no day {D}.", or "February {Y} has no day 29."
    - "The latest year you can pick is {now.Year()-19}."
    - The earliest year is 1900, with "The earliest year you can pick is 1900."
  - `avatar.Make(r io.Reader, gate *login.Gate, wait time.Duration) ([]byte, error)`. It returns PNG bytes. Its errors are `avatar.ErrTooLarge` (over 1 MiB), `avatar.ErrFormat` (not PNG, JPEG or GIF by the first bytes), `avatar.ErrDimensions` (over the pixel limits) and `login.ErrBusy`.
    - It reads at most 1 MiB + 1 byte.
    - It checks the format and calls `image.DecodeConfig` before any full decode.
    - It acquires `width × height × 4 / 1024 + 1024` KiB from the gate for the decode.
  - `board.SetAvatar(ctx, pool, v Viewer, path string) (old string, err error)` and `board.RemoveAvatar(ctx, pool, v Viewer) (old string, err error)`. Both need `account.avatar`. They return the replaced path, so the caller deletes that file after the commit.

- [ ] **Step 1: Write the failing tests**
  - `TestSetTimezone`: `America/Chicago` is stored. `Etc/GMT+4`, `UTC` and `Mars/Base` are refused, and nothing changes.
  - `TestSetDateFormat`: the three values are stored, and `custom` is refused.
  - `TestSetAboutPartial`: with only `Location` set, the stored occupation, interests and website are unchanged.
  - `TestAboutRules`: the website and length cases of Task 1's table, through `SetAbout`, with the messages above.
  - `TestBirthdayRules`: the birthday cases of Task 1's table, with `now` in 2026, through `SetBirthday`, each with its message. `Birthday{}` clears the stored birthday.
  - `TestSettingsRights`:
    - a guest gets `ErrForbidden` from each function;
    - with `account.profile` removed by the test, `SetAbout` and `SetBirthday` give `ErrForbidden`;
    - with `account.avatar` removed, `SetAvatar` and `RemoveAvatar` give `ErrForbidden`;
    - `SetTimezone` and `SetDateFormat` still work.
  - `TestMakeFormats`: a 300 × 200 PNG, a 40 × 40 JPEG and a 64 × 64 GIF each give a PNG that decodes to 128 × 128.
  - `TestMakeRefusesHugeBeforeDecode`: a hand-built PNG of under 1 KB whose header declares 50,000 × 50,000 gives `ErrDimensions`. A gate with a budget of 1 KiB sees no acquire.
  - `TestMakeOutputIsFresh`:
    - a JPEG with an APP1 segment that holds the text `SECRET-EXIF` gives output without that text;
    - a GIF that ends with `<script>` bytes after its trailer gives output without them;
    - the output starts with the PNG signature.
  - `TestMakeRefuses`: 1 MiB + 1 byte gives `ErrTooLarge`. A text file named `a.png` gives `ErrFormat`. A WebP gives `ErrFormat`.
  - `TestMakeAnimatedGIF`: a two-frame GIF gives the first frame.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/ ./internal/avatar/`. Also run a new `FuzzMake` for 60 s, and add it to the CI `fuzz` job. Expected: PASS, no new failing inputs.
- [ ] **Step 5: Commit and push.** `git commit -m "board, avatar: settings writes, and a fresh PNG for each avatar"`

**Checkpoint:** none (no pages yet).

---

### Task 6: The account page, with auto-save

**Files:**
- Modify: `internal/web/account.go`, `internal/web/account_test.go`, `internal/web/templates/account.html`, `internal/web/site.go` (routes and `saveLimits`), `internal/web/csrf.go` (`protectUpload`), `internal/web/static/gobb.js`
- Modify: `internal/web/sideeffect_test.go`, `internal/web/basepath_test.go`, `internal/web/post_test.go` (the token and CSP tables), `docs/changelog.md`

**Interfaces:**
- Consumes: Task 5's functions, `tz.Zones`, `tz.Common`, and `s.login`'s gate.
- Produces:
  - The account routes in "Decisions".
  - `protectUpload(limit int64, h http.HandlerFunc) http.Handler`. It is `protect` with `r.ParseMultipartForm(limit)` in place of `r.ParseForm`. The avatar route's limit is 1 MiB + 64 KiB, so no part spills to disk.
  - `saveAnswer(w, r, err error, field string) bool`. It writes the 204 or JSON answer when the request has `X-Gobb-Save: 1`, and reports whether it did.

Rules (preview: `account.html`):
- **The sections, in order.**
  1. Time zone: one `<select>`, "Common" first, then "All zones". The hint reads "Right now it is {now in the zone}".
  2. Date format: three radios, each with its sample in bold, made with the viewer's own clock.
  3. About you: four text boxes, then the birthday's day, month and year lists. Years run from now − 19 down to 1900.
  4. Avatar: the current one, a file input, and "Remove avatar" when one exists.
  5. Signature.
  6. Password.
  7. Email.
- **Hint texts.**
  - Birthday: "Leave out the year if you don't want your age shown. The latest year you can pick is {now − 19}. The birthday saves when it has both a day and a month, or neither."
  - Avatar: "A PNG, JPEG or GIF of at most 1 MB. It uploads as soon as you pick it. gobb checks the file, stores a fresh copy at 128 × 128 pixels, and drops everything else in the file."
- The line under the heading reads "Changes save as you make them." and links the member's profile.
- **Partial saves.** `POST /account/about` changes only the fields present in the form. The birthday changes only when `bday`, `bmonth` and `byear` are all present.
- **A stored zone outside `tz.Zones`** shows as a first option, "{zone} (your current setting)", selected.
- **`gobb.js`.**
  - It adds the auto-save of "Decisions".
  - It writes the status with `textContent` only, and adds no HTML.
  - It reads the token from the form's hidden `csrf` input.
  - It sends with `fetch(…, {method: "POST", credentials: "same-origin"})`.
  - After a successful avatar upload, it sets the avatar's `<img>` to the `src` of the answer. After a remove, it shows the initial in place of the image.
- **The avatar files.** The handler owns them.
  - It calls `avatar.Make`.
  - It writes the PNG under the data directory through `os.OpenRoot`, with `O_EXCL` and mode `0644`.
  - It calls `board.SetAvatar`.
  - When the board call fails, it deletes the new file.
  - When the board call succeeds, it deletes the old file. The old path must start with `avatars/` and hold no `..`.
- **Limits.**
  - Over the save limit: 429, "Too many changes. Wait a few minutes and try again."
  - Over the upload limit: 429, "Too many uploads. Wait an hour and try again."
  - A busy gate: 503 with `msgBusy`.

- [ ] **Step 1: Write the failing tests**
  - `TestAccountSettingsNoScript`. Each form is posted as a browser without script would post it:
    - time zone, date format, About you with the birthday, and remove the avatar each give 303 to `/account?done=…`, and the value is stored;
    - a bad website gives 400 with the page, the sent value in its box, and the message beside it.
  - `TestAutoSaveAnswers`: with `X-Gobb-Save: 1`:
    - a good time zone gives 204;
    - a bad website gives 400 with `{"field":"website","message":"Enter an address that starts with https:// or http://."}`, as `application/json`;
    - a guest gets 403 JSON.
  - `TestAutoSaveIsPartial`:
    - alice has location, occupation and website set. A save of `website` alone leaves the location and the occupation;
    - a save of `bday` and `bmonth` without `byear` leaves the birthday unchanged.
  - `TestAvatarUpload`:
    - a multipart PNG with the token gives 303 without script, and 204 with the header;
    - the stored path matches `avatars/u1-[0-9a-f]{16}\.png`;
    - the file decodes to 128 × 128;
    - the old file is gone;
    - a second upload gives a different name;
    - remove clears the path and deletes the file.
  - `TestAvatarRefusals`:
    - 2 MiB gives 413;
    - a text file gives 400 with "Pick a PNG, JPEG or GIF.";
    - a 50,000-pixel PNG gives 400 with "That image is too large. Use one of at most 8,000 pixels a side.";
    - with no token, 403.
  - `TestSaveRateLimit`: the 121st save in 10 minutes gives 429, and the value is not stored.
  - `TestAccountPageUnknownStoredZone`: with alice's zone set to `Asia/Old_Name` by SQL, `/account` shows it selected as "(your current setting)". A date format save still gives 204.
  - `TestAccountGuest`: `/account` gives 302 to `/login` for a guest, and every account POST gives 403.
  - `TestScriptHasNoHTMLSinks`: `gobb.js` holds none of `innerHTML`, `outerHTML`, `insertAdjacentHTML`, `document.write`, `eval(` and `new Function`.
  - Add the routes to `getPaths`, `TestBasePathEverywhere`, `TestPostingNeedsToken` and `TestCSPStrictEverywhere`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: the account page saves each setting as it changes"`

**Checkpoint:**
- In a headless browser on the private copy, the controller changes each setting, as on the preview.
- The controller makes two fast changes to one field, and the second value is the one stored.
- The controller uploads and removes an avatar.
- Then the owner tries the page.

---

### Task 7: Profiles and birthdays

**Files:**
- Create: `internal/board/birthdays.go`, `internal/board/birthdays_test.go`
- Modify: `internal/board/users.go` (`ProfilePage`), `internal/web/users.go`, `internal/web/pages.go` (`indexData`)
- Modify: `internal/web/templates/profile.html`, `internal/web/templates/index.html`, the tests, `docs/changelog.md`

**Interfaces:**
- Produces:
  - `ProfilePage` gains `Location, Occupation, Interests, Website string`, `Age *int` and `CanMessage bool`. `CanMessage` is true when the viewer has `pm.send`, the member is active, and the member is not the viewer. Task 9 links it.
  - `board.Birthdays(ctx, q, v Viewer, now time.Time) ([]BirthdayRow, error)`, with `BirthdayRow{ID int64; Name string; Age *int}`.

Rules (preview: `profile.html`):
- The profile shows Age, Location, Occupation, Interests and Website, in that order after "Last visit", each only when set.
- The website shows its host and path without the scheme. Its link is `rel="nofollow ugc noopener"`.
- The age is in whole years on `now` in the viewer's zone.
- **The index line.** It reads "Birthdays today: alice (42), bob". It lists active members whose day and month are today in the viewer's zone, by name.
  - A 29 February birthday shows on 28 February in a year that is not a leap year.
  - The line shows only to a viewer with `account.view_profiles`, the right that the profiles need. The names link to the profiles.

- [ ] **Step 1: Write the failing tests**
  - `TestProfileAbout`:
    - alice's profile shows her location;
    - the website link has `href="https://example.com/alice"` and `rel="nofollow ugc noopener"`, and its text is `example.com/alice`;
    - on 2026-10-25 her age is 42, on 2027-03-13 it is 42, and on 2027-03-14 it is 43;
    - bob's profile shows no age;
    - carol's shows none of the five rows.
  - `TestIndexBirthdays`:
    - on 14 March 2026, as bob, the index shows "alice (42)";
    - a guest without `account.view_profiles` sees no line;
    - a member born on 29 February shows on 28 February 2027 and on 29 February 2028.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: About you and the age on profiles, and today's birthdays"`

**Checkpoint:** the owner looks at their own profile on the private copy after setting "About you".

---

### Task 8: Private messages in the board

**Files:**
- Create: `internal/board/messages.go`, `internal/board/messages_test.go`
- Modify: `internal/board/write.go` (a `textKind` inside `ParseBodyKeeping`, and `ParseMessage`), `docs/changelog.md`

**Interfaces:**
- Consumes: `ParseBodyKeeping`, `account.CleanUsername`, `inTx`.
- Produces:
  - `board.ParseMessage(ctx, q, v Viewer, src string) (Body, error)`.
    - The flags are `pm.markdown` and `pm.smilies`, with URL linking on.
    - An image needs `pm.images`, and the refusal is "You may not put images in messages."
    - The limit is 60,000 bytes.
  - `board.MessageRow{ID int64; Subject string; From Author; To []Author; At time.Time; Unread bool}` and `board.MessageList{Rows []MessageRow; Page, Pages int}`.
  - `board.Inbox(ctx, q, v Viewer, page int) (MessageList, error)` and `board.Sent(ctx, q, v Viewer, page int) (MessageList, error)`, 25 rows a page.
  - `board.Message{ID int64; Subject string; From Author; To []Author; Bcc []Author; At time.Time; Tree, Signature *markup.Node; Flags markup.Options; Mine bool}`.
  - `board.ReadMessage(ctx, pool, v Viewer, id int64) (Message, error)`. It sets `is_read` on the viewer's recipient row.
  - `board.UnreadMessages(ctx, q, v Viewer) (int, error)`.
  - `board.MessageDraft{To, Subject, Source string; IP netip.Addr}` and `board.SendMessage(ctx, pool, v Viewer, d MessageDraft, now time.Time) (int64, error)`.
  - `board.DeleteMessage(ctx, pool, v Viewer, id int64) error`.
  - `board.ReplyDraft(ctx, q, v Viewer, id int64, quote bool) (MessageDraft, error)`.

Rules:
- **Visibility.** A party is the author while `author_deleted` is false, or a recipient while `is_deleted` is false. Every function gives `ErrNotFound` to anyone else.
- **Recipients.**
  - `To` lists the recipients of kind `to`.
  - `Bcc` is filled for the author, and for a blind-copy recipient, with that recipient only.
  - A gone author shows `author_name` with no link.
- **The To field.**
  - Names are separated by commas, trimmed, matched with `account.CleanUsername`, and taken once each.
  - Unknown, or not active: "No member named “{name}”." on field `to`.
  - The viewer: "You cannot send a message to yourself."
  - None: "Add at least one member."
  - Over 10: "Send to at most 10 members."
  - More than one without `pm.send_many`: `ErrForbidden`.
- **The stored message.**
  - `author_name` is the viewer's name, and `author_ip` is the address.
  - The flags come from `ParseMessage`, and `enable_sig` is true (spec 17: new writing carries the signature).
  - Each recipient gets a row of kind `to`.
- **Duplicates.** A message whose author, recipients, subject and source equal one that the author sent in the last 60 seconds is not saved again. The earlier ID comes back.
- **Delete.** The viewer's recipient row gets `is_deleted`, and the message gets `author_deleted` when the viewer wrote it. Both happen when both apply. No row is removed.
- **Reply drafts.**
  - `To` is the author's name. A gone author gives an empty `To`.
  - The subject is "Re: " and the subject, unless it starts with "Re: " in any case.
  - With `quote`, the source is `> **{author}** wrote:` and the quoted lines.

- [ ] **Step 1: Write the failing tests** (fixture PMs: 1 alice to bob, bcc carol; 2 bob to alice, author deleted)
  - `TestInboxAndSent`:
    - bob's inbox holds message 1, unread;
    - alice's inbox holds message 2;
    - bob's sent list does not hold message 2;
    - alice's sent list holds message 1.
  - `TestReadMarksRead`: bob reads message 1, and his `UnreadMessages` goes from 1 to 0.
  - `TestMessageNotFoundForOthers`:
    - carol gets `ErrNotFound` for message 2, from `ReadMessage`, `DeleteMessage` and `ReplyDraft`;
    - bob, its author, gets `ErrNotFound` for message 2, since he deleted it.
  - `TestBccHidden`: on message 1, bob's `Bcc` is empty, carol's `Bcc` holds only carol, and alice's holds carol.
  - `TestSendRules`, one case for each refusal above. Also:
    - a subject of 121 characters is refused on field `subject`;
    - a member without `pm.send` gets `ErrForbidden`.
  - `TestSendStores`: alice sends "Lunch?" to bob and carol (with `pm.send_many`). Both inboxes hold it, unread, and `enable_sig` is true.
  - `TestMessagePayloadsSafe`: each line of `markup/testdata/xss/payloads.txt` as a body stores a tree whose HTML passes `htmlcheck.Check`.
  - `TestDeleteOwnView`: bob deletes message 1. His inbox loses it, and alice's sent list and carol's inbox keep it.
  - `TestDuplicateMessageIsOne`: two equal sends within 60 s give one ID and one row.
  - `TestReplyDraft`: "Re: Welcome aboard" once, even when replying to a reply. The quote starts `> **alice** wrote:`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "board: private messages: read, send, reply, quote and delete"`

**Checkpoint:** on the private copy, the controller counts each member's inbox and sent rows. The totals match the imported recipient and author rows.

---

### Task 9: The message pages

**Files:**
- Create: `internal/web/messages.go`, `internal/web/messages_test.go`, `internal/web/templates/messages.html`, `internal/web/templates/message.html`, `internal/web/templates/message-new.html`
- Modify: `internal/web/site.go` (routes, the Messages nav link with `Badge`, `pageFiles`, `noindexPages`), `internal/web/templates/profile.html` ("Send a message")
- Modify: the shared test tables, `docs/changelog.md`

**Interfaces:**
- Consumes: Task 8's functions, Task 4's `navLink.Badge`, Task 7's `ProfilePage.CanMessage`, Plan 4's confirm page.
- Produces: the message routes in "Decisions".

Rules (preview: `messages.html`, `messages-sent.html`, `message.html`, `message-new.html`):
- **The lists.** Tabs for Inbox and Sent. Each row shows the subject (bold when unread), the other party, and the time in the viewer's format.
- **One message.** It shows the subject, From, To, Bcc where the viewer may see it, the time, the body rendered like a post (`addRendered` for its embeds), and the signature. Under it: Reply, Quote and Delete, each only with its right.
- **The compose form.**
  - It has To, Subject and the text box, with Plan 4's Markdown line and "Send" and "Preview" buttons.
  - A refusal answers 400 with every value kept.
  - Success: 303 to `/messages/{id}`.
- **Nav.** "Messages" with `Badge` "N new" from `UnreadMessages`. A failure to count logs a WARN line and shows no badge.
- **Profile.** "Send a message" links to `/messages/new?to={name}` when `CanMessage` is true.

- [ ] **Step 1: Write the failing tests**
  - `TestMessagesFlow`:
    - alice opens bob's profile, follows "Send a message", sends "Lunch?", and gets 303;
    - bob's nav shows "Messages" with "1 new";
    - bob opens it, and the badge is gone;
    - bob quotes it and sends, and alice's inbox shows "Re: Lunch?";
    - alice opens it and sees a `blockquote` with "alice wrote".
  - `TestMessagePagesNotFound`: carol gets 404 for `/messages/2`, `/messages/2/reply`, `/messages/2/reply?quote=1` and a POST to `/messages/2/delete`. No body holds the subject of message 2.
  - `TestMessagesNeedSignIn`: a guest gets 302 to `/login` for each GET, and 403 for each POST.
  - `TestMessagesNoStore`: each message page has `Cache-Control: private, no-store` and the robots `noindex`.
  - `TestMessageFormKeepsText`: an unknown recipient gives 400. The form holds the subject, the body and the message "No member named “nobody”.".
  - `TestMessagePayloadsPage`: each payload of the corpus is sent to bob. On bob's message pages, every `.post-body` passes `htmlcheck.Check`, and the CSP equals that of a page with no payloads.
  - `TestMessageSendLimit`: the 11th send in 10 minutes gives 429 with the form kept.
  - Add the routes to `getPaths`, `TestBasePathEverywhere`, `TestPostingNeedsToken`, `TestCSPStrictEverywhere` and the route table test.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: inbox, sent list, message pages and the compose form"`

**Checkpoint:**
- On the private copy, the controller reads its made-up member's messages and sends one to a second made-up member.
- The owner signs in with a real account and reads the old messages. The owner sends nothing to a real member.

---

### Security review (controller, after Task 9)

The controller dispatches a fresh Opus reviewer. Its whole scope is security, over the range from Task 1's base to Task 9's head. **(owner: security is paramount)**

It checks:
- **Upload.** The avatar path for bombs, polyglots, path tricks in the stored name, the race between two uploads, and the deletion of the old file.
- **Auto-save.** Every auto-save answer for CSRF and Cross-Origin protection, and the JSON answers for any echo of request text.
- **Messages.** Every message read and write for IDs changed in the URL or the form, `bcc` leaks, and a deleted message that shows again.
- **About you and messages.** The "About you" fields and the website link, through every page that shows them. The message bodies through the payload corpus.
- **The bundle.** The version 2 checks, for a zone, a website or a birthday that slips through.

It may run the server and attack it. Its Critical and Important findings get one fix round, and a scoped re-review, before Task 10 starts.

---

### Task 10: Release 0.2.0 and the rehearsal

**Files:**
- Modify: `docs/changelog.md` (date the `0.2.0` section), `internal/version/version.go`, `README.md` (the bundle version, the account page and messages)

- [ ] **Step 1: Bump the version.** Set `internal/version` to `0.2.0`, and date the changelog section. Run `go run ./scripts/releasecheck -dry-run`. Expected: it passes. With a tag, the command takes the flags first: `go run ./scripts/releasecheck v0.2.0`. Go stops flag parsing at the first argument.
- [ ] **Step 2: Commit and push.** `git commit -m "release: 0.2.0"`
- [ ] **Step 3: Tag `v0.2.0`, with the owner's go-ahead at the time.** Watch the release workflow until the image smoke-runs and prints `0.2.0`.

**Checkpoint (controller and owner):** the rehearsal runs on the production host, from the owner's private notes, with each step on the owner's go-ahead:
- the converter at format version 2;
- a fresh bundle;
- the import into the 0.2.0 image;
- the owner signs in, reads the old messages, sets a time zone, and sees the avatar.

---

### Final review (controller)

Once Task 10 is green, the Opus reviewer reviews the whole plan's work against the spec sections in the header and the Review Focus. Then one fix dispatch, and one scoped re-review.

## Order

The tasks run in order, one at a time.
- Task 2 needs Task 1.
- Task 3 needs Task 2's `date_format`.
- Task 4 stands alone, but it lands before Task 6, which uses its CSS.
- Task 6 needs Task 5.
- Task 7 needs Task 2.
- Task 9 needs Tasks 4, 7 and 8.
- The security review comes after Task 9, and Task 10 after the review's fixes.
- The private converter's own plan must land before Task 10's rehearsal.
