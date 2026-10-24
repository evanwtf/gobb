# gobb Plan 2: Import — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Define gobb's import bundle, ship the public `bundle` package that reads, writes and checks it, load a bundle into an empty database with `gobb import`, and export every user's effective permissions with `gobb perms export`.

**Architecture:** `bundle` is a public package at the module root, next to `markup`, because converters import it. It holds one Go type per bundle file, a streaming reader with strict decoding, a writer, and `Check`, which proves a whole bundle consistent without a database. `internal/importer` loads a checked bundle in one transaction and stages the files in the data directory, then moves them into place after the commit. `internal/acldb` builds an `acl.Subject` from the database. Plan 3 reuses it for every request.

**Tech Stack:** Go 1.27; PostgreSQL 16+; pgx v5 (`CopyFrom` for bulk rows); standard library `archive/tar`, `compress/gzip`, `encoding/json`, `crypto/sha256`; `golang.org/x/text` (`cases`, `unicode/norm`). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md` (sections 8, 9, 10, 12.1, 14, 15). Roadmap: `docs/superpowers/plans/2026-10-23-gobb-roadmap.md`, including "Carried from Plan 1".

**Workers:** Sonnet, high effort, unless a task says "Worker: Haiku, max effort". Reviewer: Opus, high effort, after every task and over the whole plan at the end.

## Global Constraints

- **Clean room.** Work only from this repo and public standards. Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. If a task needs a fact about another system, stop and ask. In code, docs and tests, call the system a bundle comes from "the source system". (`AGENTS.md`; spec 1.)
- Module `github.com/evanwtf/gobb`; build with the latest Go 1.27.x. No new dependencies (spec 15).
- This repo is public. Never commit hostnames, IP addresses other than `10.x` and the IPv6 documentation prefix `2001:db8::/32`, home-directory paths, secret values, or any forum data. Tests and the example bundle use made-up data only.
- Commit subjects: `area: imperative summary`. No agent session URLs or IDs, no agent trailers.
- Work lands on `main`; push after every task. Each task adds its bullet to `docs/changelog.md` under `## 0.1.0 (unreleased)`.
- Tests that need PostgreSQL use `dbtest.New` and skip when `GOBB_TEST_DATABASE_URL` is unset. CI sets it and fails on any skip.
- Logs go through `log/slog` to stdout. Command results print to stdout.
- Spec 8.1, verbatim: "Unknown fields, unknown enum values, broken references, and a count that disagrees with the manifest all stop the import. gobb does not guess at data it does not model."
- Every error that a bundle causes names the file, the line and the field, in the form `posts.jsonl:12: body.source: ...`. A converter fixes its output from that message alone.
- Exported names in `bundle` get doc comments. `bundle` may import `markup` and gobb's `internal` packages, but not `internal/db` or pgx: a converter must use it without a database.

## Review Focus

1. **Invalid UTF-8 or NUL in a bundle line.** `encoding/json` silently turns invalid UTF-8 into U+FFFD, and PostgreSQL cannot store U+0000. The reader must refuse both and name the file and line. Test: `TestEachRefusesInvalidUTF8AndNUL` (Task 3).
2. **A hostile archive.** A `.tar.gz` with `../` paths, absolute paths, symlinks, hard links or device files must fail before anything lands outside the destination. Test: `TestExtractRefusesUnsafeEntries` (Task 3).
3. **An import that fails half-way.** A bad row late in `posts.jsonl`, or an error after the files are staged, must leave the database empty and the data directory as it was. Test: `TestImportIsAllOrNothing` (Task 6).
4. **Running import over a loaded board.** Without `--replace`, the import refuses and changes nothing. With `--replace`, it moves the old files aside and never deletes them. Tests: `TestImportRefusesNonEmpty`, `TestImportReplaceKeepsOldFiles` (Task 5).
5. **Two usernames that clean to the same name** (`Bob` and `bob`, or a fullwidth `Ｂｏｂ`). `Check` must refuse the bundle and name both user IDs, because one of those members could not log in. Test: `TestCheckUsernameCollision` (Task 4).

---

## Decisions this plan fixes

**Site paths in content.** Stored Markdown reaches gobb pages through site paths, so these short forms are part of the bundle contract. Plan 3 serves each one. Each path is relative to the board's base path: with base path `/forum/`, `/p/5` means `/forum/p/5`. Plan 3 makes `Render` add the base path.

| Path | Target |
|---|---|
| `/p/<post id>` | the post, on its topic page (`#p<id>`) |
| `/t/<topic id>` | the topic |
| `/f/<forum id>` | the forum |
| `/u/<user id>` | the profile |
| `/a/<attachment id>` | the attachment's file. An image whose URL is this path shows the attachment inline (spec 12.1). |

**Data directory.** The importer owns five directories under `GOBB_DATA_DIR`: `attachments/`, `avatars/`, `smilies/`, `ranks/`, `header/`. A stored path (`attachments.stored_path`, `users.avatar_path`, `smilies.image_path`, `ranks.image_path`, and the `header_image` setting) is the bundle's `files/` path, for example `attachments/12.png`. The file lives at `GOBB_DATA_DIR/<path>`.

**Values gobb computes, not the bundle.** A converter cannot get these wrong, because the bundle does not carry them:
- `posts.forum_id`, from the post's topic;
- `posts.body_tree` and `posts.body_text`, and the same columns for private messages, signatures and static pages, from `markup.Parse` and `markup.Render`;
- `users.username_clean` and `users.email_clean` (Task 1);
- the topic and forum counters and last-post pointers (Task 6).

The bundle does carry `users.post_count`, `topics.view_count`, poll option vote counts and attachment download counts. gobb cannot rebuild those.

---

## The bundle format, version 1

Task 2 turns this section into `docs/bundle.md` and into the types in `bundle/`. Field names are the JSON names. "id" means a positive `int64`. "time" means an RFC 3339 string with an offset. "?" marks a field that may be `null`. Every other field is required. A missing field and an unknown field are both errors.

**Layout:**
- `manifest.json`
- `settings.json`
- the 23 data files below, each as `<name>.jsonl`, present even when empty
- `files/`

Nothing else may appear at the top level.

**String rules for every field.**
- The line must be valid UTF-8. No string may contain U+0000.
- A string is at most 100,000 bytes unless the tables say otherwise.
- A "name" is 1 to 255 bytes, with no control characters, after trimming spaces.
- A "label" is at most 255 bytes, with no control characters.
- A "slug" matches `^[a-z0-9][a-z0-9-]{0,63}$`.
- A "param" matches `^[A-Za-z0-9_]{1,64}$`.

A line is at most 1 MiB.

**Shared shapes.**

| Shape | Fields |
|---|---|
| `file` | `path`: a slash-separated path under `files/`. It passes `fs.ValidPath`, holds no backslash, is at most 255 bytes, and its first element is the directory the field names. `size`: bytes, ≥ 0. `sha256`: 64 lowercase hex characters. |
| `body` | `source` (Markdown, at most 100,000 bytes), `markdown`, `smilies`, `autolink` (bools). |
| `setting` | `"yes"`, `"no"` or `"never"`. |

**`manifest.json`.** `format`: `"gobb-bundle"`. `version`: `1`. `created_at`: time. `source`: a label. `counts`: an object with exactly the 23 data-file names as keys (no `.jsonl`), each with the file's line count.

**`settings.json`.**
- `board_name`: a name.
- `board_description`: a label.
- `timezone`: an IANA zone that `time.LoadLocation` accepts. Guests see times in it.
- `default_color_scheme`: `slate`, `slate-deep` or `slate-light`.
- `registration`: `closed`, `open` or `approval`.
- `email_notifications`: bool.
- `header_image`?: a `file` under `header/`.

**Data files, in load order.**

| File | Fields | Rules |
|---|---|---|
| `ranks` | `id`, `title` (label), `min_posts` (≥ 0), `special` (bool), `image`? (`file` under `ranks/`) | |
| `users` | `id`, `username` (name), `email` (label; may be empty), `password` {`scheme`: `legacy` or `gobb`, `hash`}, `state` (`active` or `inactive`), `owner`, `email_verified` (bools), `registered_at` (time), `last_visit_at`? (time), `last_mark_at`? (time), `post_count` (≥ 0), `rank_id`? (ranks), `avatar`? (`file` under `avatars/`), `signature` (`body`), `timezone` (IANA zone), `color_scheme` (as in settings), `notify_on_post` (bool) | `password.Recognized(scheme, hash)` holds. Cleaned usernames are unique. At least one active user is an owner. |
| `gone_users` | `id` | Not also a user ID. |
| `groups` | `id`, `name` (name), `builtin`? (`guests`, `members`, `moderators` or `admins`), `kind` (`open`, `request`, `closed`, `hidden` or `builtin`), `description` | `kind` is `builtin` exactly when `builtin` is set. Each of the four built-in names appears exactly once. |
| `group_members` | `group_id`, `user_id`, `leader`, `pending` (bools) | The pair is unique. |
| `roles` | `id`, `name` (name), `kind` (`forum`, `mod` or `board`), `description`, `position` (int), `settings` (a list of {`option`, `setting`}) | Each option is in `acl.Options` and appears once per role. A `forum` role holds Forum options only, a `mod` role Moderator options only, a `board` role Board options only. |
| `grants` | `group_id`? or `user_id`?, exactly one; `forum_id` (0 = board-wide, else a forum); `role_id`? or (`option`? and `setting`?), exactly one | An option grant fits its scope: a Forum option needs a forum, a Board option needs 0, and a Moderator option fits both. A role grant fits the same way by role kind. |
| `forums` | `id`, `parent_id`? (forums), `kind` (`category`, `forum` or `link`), `name` (name), `description`, `rules`, `link_url`? , `locked` (bool), `position` (int) | `link_url` is set exactly when `kind` is `link`, and passes `markup.CheckLinkURL`. The parent chain has no cycle. |
| `topics` | `id`, `forum_id` (a forum of kind `forum`), `title` (label), `author_id`? (users), `author_name` (label), `created_at` (time), `view_count` (≥ 0), `kind` (`normal`, `sticky`, `announce` or `global`), `locked` (bool), `visibility` (`approved`, `unapproved` or `deleted`), `moved_to`? (topics) | A topic with `moved_to` is a moved-topic marker. It has no posts, and its target is not itself a marker. Every other topic has at least one post. |
| `posts` | `id`, `topic_id` (topics), `author_id`? (users), `author_name` (label), `author_ip`? (an IP address), `created_at` (time), `subject` (label), `body` (`body`), `show_signature` (bool), `visibility` (as topics), `edit_count` (≥ 0), `edited_at`? (time), `edited_by`? (users), `edit_reason` (label) | |
| `polls` | `topic_id` (topics; unique), `question` (label), `max_options` (1 to the number of options), `ends_at`? (time), `can_change_vote` (bool), `options` (1 to 100 of {`id` (≥ 1, unique in the poll), `text` (label), `votes` (≥ 0)}) | |
| `poll_votes` | `topic_id`, `option_id` (an option of that poll), `user_id` (users), `voter_ip`? (an IP address) | The triple is unique. An option has no more vote rows than its `votes`. A user has no more vote rows in a poll than its `max_options`. |
| `attachments` | `id`, `post_id`? (posts; null for an upload that no post uses), `position` (≥ 0), `uploader_id` (users), `name` (label), `file` (`file` under `attachments/`), `mime` (`image/png`, `image/jpeg`, `image/gif` or `image/webp`), `width`?, `height`? (≥ 1), `download_count` (≥ 0), `comment`, `created_at` (time) | |
| `pms` | `id`, `author_id` (users), `author_ip`? (an IP address), `created_at` (time), `subject` (label), `body` (`body`), `show_signature`, `author_deleted` (bools), `recipients` (1 or more of {`user_id` (users), `kind` (`to` or `bcc`), `read`, `deleted` (bools)}) | A user appears once per message. |
| `topic_watches` | `user_id`, `topic_id`, `notify_pending` (bool) | The pair is unique. |
| `bookmarks` | `user_id`, `topic_id` | The pair is unique. |
| `topic_marks` | `user_id`, `topic_id`, `marked_at` (time) | The pair is unique. |
| `forum_marks` | `user_id`, `forum_id`, `marked_at` (time) | The pair is unique. |
| `bans` | `kind` (`user`, `email` or `ip`), `user_id`? (users), `email`? (label), `ip`? (a CIDR prefix with no host bits set), `reason`, `reason_shown` (labels), `created_at` (time), `ends_at`? (time) | Exactly the field that matches `kind` is set. |
| `smilies` | `code`, `image` (`file` under `smilies/`), `description` (label), `position` (int) | `code` is unique, and it is valid as the `code` of a `markup` `smiley` element. |
| `legacy_routes` | `path` (1 to 255 bytes; no leading `/`, no `?` or `#`), `match` (an object of up to 10 param → label pairs), `id_param`? (param), `target_kind` (`home`, `forum`, `topic`, `post`, `user`, `members`, `team`, `attachment`, `avatar`, `search`, `account` or `page`), `target_id`? (id), `target_slug`? (slug), `offset_param`? (param), `position` (int) | `forum`, `topic`, `post`, `user`, `attachment` and `avatar` need exactly one of `id_param` and `target_id`. `page` needs `target_slug`, which names a static page, and nothing else. The other kinds need neither. `offset_param` is allowed only for `forum` and `topic`. A fixed `target_id` exists (`avatar` and `user` → users). The pair (`path`, `match`) is unique. |
| `legacy_files` | `prefix` (1 to 255 bytes; no leading `/`; ends with `/`), `dir` (`smilies` or `ranks`) | `prefix` is unique. |
| `static_pages` | `slug` (slug), `title` (name), `body` (`body`), `updated_at` (time) | `slug` is unique. |

**Files.** Every file that a row names exists under `files/` as a regular file, with the stated size and SHA-256. Rows that name the same path agree on size and SHA-256. Every file under `files/` is named by at least one row.

---

### Task 1: Password recognition and name cleaning

**Worker: Haiku, max effort.**

**Files:**
- Create: `internal/password/recognize.go`, `internal/password/recognize_test.go`
- Create: `internal/account/clean.go`, `internal/account/clean_test.go`

**Interfaces:**
- Consumes: the unexported hash parsers in `internal/password` (`parsePHC`, `bcryptCostOK`, `phpassIndexOK`, and the chain format in `chainMatches`).
- Produces:
  - `password.Recognized(scheme, hash string) bool`. It reports whether `Verify` can ever accept the hash, without running a hash.
    - `gobb`: an argon2id PHC string that `parsePHC` accepts.
    - `legacy`: the empty string (it never matches, but it is a known value), or a bcrypt hash with an allowed cost, a legacy `$argon2id$` string, or a chain string with an allowed phpass index.
    - Any other scheme returns false.
  - `account.CleanUsername(s string) string`: NFKC, then `cases.Fold()`, then trim Unicode spaces. Plan 3's login uses the same function.
  - `account.CleanEmail(s string) string`: trim spaces, then lowercase.

- [ ] **Step 1: Write the failing tests**
  - `TestRecognized`:
    - True: `password.Hash("x")` under `gobb`; the empty string under `legacy`; a bcrypt cost-10 hash under `legacy`; one `$chain$` row from `testdata/legacy-vectors.json`.
    - False: a bcrypt hash under `gobb`; `md5:abc` under `legacy`; bcrypt cost 15; `$argon2id$v=19$m=999999999,t=1,p=1$…`; any hash under scheme `plain`.
  - `TestRecognizedIsFast`: 1,000 calls on a bcrypt cost-14 hash take under 50 ms in total, so no hash ran.
  - `TestCleanUsername`: `Bob`, `bob`, ` BOB ` and fullwidth `Ｂｏｂ` all give `bob`. `Straße` and `STRASSE` give the same value.
  - `TestCleanEmail`: ` A@Example.COM ` gives `a@example.com`.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/password/ ./internal/account/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Reuse the existing parsers, and refactor them only as far as `Recognized` needs.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./internal/password/ ./internal/account/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "password: recognize stored hashes; account: clean names" && git push`

---

### Task 2: The bundle types and the format document

**Files:**
- Create: `bundle/doc.go`, `bundle/types.go`, `bundle/fields.go` (per-row checks), `bundle/types_test.go`, `bundle/doc_test.go`
- Create: `docs/bundle.md`

**Interfaces:**
- Consumes: `password.Recognized`, `acl.Lookup`, `markup.CheckLinkURL` and `markup.Validate`.
- Produces, in package `bundle`:
  - `const Format = "gobb-bundle"`, `const Version = 1`, `const MaxLineBytes = 1 << 20`, `const MaxSourceBytes = 100_000`.
  - `var Files []string`: the 23 data-file names in load order, as in the table above.
  - `type Row interface{ DataFile() string }`. Each data-file type implements it and returns its file name without `.jsonl`.
  - One exported struct per data file: `Rank`, `User`, `GoneUser`, `Group`, `GroupMember`, `Role`, `Grant`, `Forum`, `Topic`, `Post`, `Poll`, `PollVote`, `Attachment`, `PM`, `TopicWatch`, `Bookmark`, `TopicMark`, `ForumMark`, `Ban`, `Smiley`, `LegacyRoute`, `LegacyFile`, `StaticPage`.
  - Shared types: `FileRef{Path string; Size int64; SHA256 string}` (the `file` shape), `Body{Source string; Markdown, Smilies, Autolink bool}`, `Password{Scheme, Hash string}`, `RoleSetting{Option string; Setting Setting}`, `PollOption`, `Recipient`, `Setting` (a string type with `Yes`, `No`, `Never`). Also `Manifest{Format string; Version int; CreatedAt time.Time; Source string; Counts map[string]int}` and `Settings`.
  - A nullable field is a pointer. JSON tags match the format exactly.
  - `type Error struct{ File string; Line int; Field, Msg string }`. Its `Error()` returns `file:line: field: msg`. It drops `:line` when `Line` is 0, and `field: ` when `Field` is empty.
  - Each row type has an unexported `check() []*Error` for the rules that need only that row: string rules, enums, ranges, the exactly-one rules, scope fit, and file path shape. It leaves `File` and `Line` empty; the reader fills them.
  - `docs/bundle.md`: the format section above as prose and tables, plus one example line per file. It never names another forum product.

- [ ] **Step 1: Write the failing tests**
  - `TestRowFiles`: `Files` has 23 unique names, and for each type `T`, `T{}.DataFile()` is in `Files`.
  - `TestRowJSONRoundTrip`: one full row per type, with every pointer set, marshals and unmarshals to an equal value. A row with every pointer nil marshals each nullable field as `null`.
  - `TestCheckFields`: a table of one bad row per rule, each with the `Field` it must name. It covers:
    - a username that is only spaces (`username`);
    - a password scheme of `md5` (`password.scheme`);
    - a group with kind `builtin` and no `builtin` (`kind`);
    - a role of kind `forum` holding `admin` (`settings[0].option`);
    - a board-wide grant of `forum.read` (`option`);
    - a grant with both `group_id` and `user_id` (`group_id`);
    - a link forum with `javascript:x` (`link_url`);
    - an attachment with mime `text/html` (`mime`);
    - a file path `../x.png` (`file.path`);
    - a file path under the wrong directory (`file.path`);
    - an ip ban `10.0.0.1/8`, which has host bits set (`ip`);
    - a legacy route of kind `post` with both `id_param` and `target_id` (`id_param`);
    - a smiley code with a tab (`code`);
    - a static page slug `Bad Slug` (`slug`);
    - a body source of 100,001 bytes (`body.source`);
    - an unknown timezone (`timezone`).
  - `TestCheckFieldsAcceptsValid`: one valid row per type gives no errors.
  - `TestDocCoversEveryField` (`doc_test.go`): for every type in `Files` order, `docs/bundle.md` has a `## <name>.jsonl` heading, and each JSON field name of that type, found by reflection, appears in that section.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./bundle/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** the types, the checks and `docs/bundle.md`. `doc.go` says three things: the package's purpose, that `docs/bundle.md` is the format reference, and that a converter writes with `Writer` and proves its output with `Check` before it ships a bundle.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./bundle/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "bundle: types, field checks, and the format document" && git push`

---

### Task 3: Reader, writer and archives

**Files:**
- Create: `bundle/reader.go`, `bundle/writer.go`, `bundle/archive.go`, `bundle/reader_test.go`, `bundle/writer_test.go`, `bundle/archive_test.go`

**Interfaces:**
- Consumes: Task 2's types and `check` methods.
- Produces:
  - `Open(dir string) (*Reader, error)`. It reads and checks `manifest.json` and `settings.json`, with unknown fields refused. It refuses a wrong format or version, a top-level entry that is not in the layout, a missing data file, and a `counts` object that lacks a file or names an extra one.
  - `(*Reader).Dir() string`, `Manifest() Manifest`, `Settings() Settings`.
  - `Each[T Row](r *Reader, fn func(line int, row T) error) error`. It streams `<name>.jsonl` one line at a time and never holds the file in memory. For each line it:
    - refuses a line over `MaxLineBytes`, invalid UTF-8, or an empty line;
    - decodes with `DisallowUnknownFields`, and refuses trailing data after the value;
    - refuses U+0000 in any string, found by a reflection walk over the decoded value;
    - runs `check`, and fills `File` and `Line` on each error;
    - calls `fn`.

    At the end of the file it compares the line count with the manifest. It returns the first error as `*Error`.
  - `Create(dir, source string, s Settings) (*Writer, error)`. `dir` must not exist, or must be empty.
  - `Add[T Row](w *Writer, row T) error`. It runs `check` first and refuses a bad row.
  - `(*Writer).AddFile(src, path string) (FileRef, error)`. It copies `src` to `files/<path>` and returns the ref with its size and SHA-256.
  - `(*Writer).Close() error`. It writes every data file, empty when it has no rows, then `manifest.json` with the counts and `created_at` in UTC.
  - `Pack(dir, archive string) error`: writes a gzip-compressed tar of `dir`, with paths relative to it.
  - `Extract(archive, dest string) error`. `dest` must not exist, or must be empty. It writes only regular files and directories. It refuses absolute paths, any path that `fs.ValidPath` rejects, symlinks, hard links and device entries, and it stops at 8 GiB in total.

- [ ] **Step 1: Write the failing tests**
  - `TestWriteThenRead`: write one row of each type, plus one file, with `Writer`. `Open` the result, and read every file with `Each`. Every row comes back equal, in order, with line numbers from 1. `Manifest().Counts` matches.
  - `TestOpenRefuses`: one case per rule: wrong `format`, `version` 2, an extra top-level file `notes.txt`, a missing `users.jsonl`, a `counts` key missing, an unknown field in `settings.json`.
  - `TestEachRefusesInvalidUTF8AndNUL`: a line with byte `0xff` inside a string gives `users.jsonl:2: invalid UTF-8`. A line with `\u0000` in a username gives an error that names `username`.
  - `TestEachRefusesBadLines`:
    - an unknown field (the error names it);
    - a line over 1 MiB;
    - an empty line;
    - two JSON values on one line;
    - `"state":"banned"` (it names `state`);
    - a count that differs from the manifest (`posts.jsonl: 3 rows, manifest says 4`).
  - `TestEachStopsOnCallbackError`: `fn`'s error comes back unchanged.
  - `TestAddRefusesBadRow`: `Add` with an invalid row returns the error, and the file is unchanged.
  - `TestPackExtractRoundTrip`: pack a written bundle, extract it to a new directory, and every file is byte-identical.
  - `TestExtractRefusesUnsafeEntries`: one archive per case, built in the test with `archive/tar`: `../evil`, `/abs`, a symlink, a hard link, and a character device. Each case returns an error, and no file appears outside `dest`.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./bundle/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Read lines with `bufio.Reader.ReadSlice` and handle `bufio.ErrBufferFull`, so that one line, not the whole file, bounds memory.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./bundle/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "bundle: streaming reader, writer, and safe archives" && git push`

---

### Task 4: Whole-bundle check, test builder and the example bundle

**Files:**
- Create: `bundle/check.go`, `bundle/check_test.go`
- Create: `internal/bundletest/bundletest.go`
- Create: `docs/example-bundle/` (generated, then committed), `bundle/example_test.go`

**Interfaces:**
- Consumes: Tasks 1 to 3.
- Produces:
  - `Check(r *Reader) error`. It reads every file once, in `Files` order, and collects only IDs and keys, never row bodies. It then proves every rule in the format tables that spans more than one row: unique IDs and keys, references, the built-in groups, at least one active owner, cleaned-username uniqueness, the forum parent cycle, the topic marker rules, the poll vote limits, the legacy route targets, and the files rule. It returns `nil`, or an `Errors` value (`type Errors []*Error`, whose `Error()` joins the lines) holding up to 100 errors in file order.
  - `bundletest.Board` holds a slice per row type, plus `Settings` and `Files map[string][]byte` (path → content).
  - `bundletest.Small() *Board` returns a valid board:
    - 3 forums: a category, a forum and a subforum;
    - 4 users, one of them an owner, and 1 gone user;
    - the 4 built-in groups, and 1 extra group with a pending member;
    - 2 roles, with grants that include a `never`;
    - 3 topics: 1 sticky, 1 with a poll and 2 votes, 1 moved-topic marker;
    - 6 posts: 1 guest post, 1 unapproved post, 1 post with an attachment shown inline at `/a/<id>`, and 1 with a smiley;
    - 1 orphan attachment, 2 PMs, a watch, a bookmark, both kinds of read mark, all three kinds of ban, 1 rank with an image, 2 smilies, 3 legacy routes, 1 legacy file prefix, and 2 static pages.

    The files are tiny valid images written as byte literals.
  - `bundletest.Write(t testing.TB, b *Board) string` writes `b` with `bundle.Writer` into `t.TempDir()` and returns the directory.

- [ ] **Step 1: Write the failing tests**
  - `TestCheckSmallBoard`: `Check` on `Write(Small())` returns nil.
  - `TestCheckRefuses`: one case per cross-row rule. Each case mutates `Small()`, and its error names the file, the line and the field:
    - a duplicate user ID;
    - a user ID that is also a gone user;
    - a post whose `topic_id` does not exist;
    - an `edited_by` that does not exist;
    - a missing `admins` group;
    - no owner;
    - a forum cycle;
    - a topic in a category;
    - a non-marker topic with no posts;
    - a marker with a post;
    - a marker that points to a marker;
    - a vote for an option the poll lacks;
    - 3 vote rows on an option with `votes: 2`;
    - a recipient who is not a user;
    - a route to a missing static page;
    - a fixed route target that does not exist;
    - a file whose SHA-256 differs;
    - a file under `files/` that no row names;
    - two rows that name one path with different sizes.
  - `TestCheckUsernameCollision`: users 7 `Bob` and 9 `ｂｏｂ` give one error that names both IDs.
  - `TestCheckCollectsManyErrors`: 150 posts with a missing topic give exactly 100 errors.
  - `TestExampleBundle` (`example_test.go`): `Write(Small())` equals `docs/example-bundle/` byte for byte, except `created_at`. With `-update`, it rewrites that directory. `Check` on the committed copy returns nil.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./bundle/ ./internal/bundletest/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Generate the example with `go test ./bundle/ -run TestExampleBundle -update`, and read it once to confirm it holds only made-up data.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./bundle/ ./internal/bundletest/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "bundle: whole-bundle check and an example bundle" && git push`

---

### Task 5: The importer: frame, files, people, permissions and forums

**Files:**
- Create: `internal/importer/importer.go`, `internal/importer/files.go`, `internal/importer/people.go`, `internal/importer/importer_test.go`
- Modify: `internal/db/db.go` (add `DataTables`)

**Interfaces:**
- Consumes: `bundle` (Tasks 2 to 4), `bundletest`, `account.CleanUsername`, `account.CleanEmail`, `markup.Parse`, `markup.Render`, `markup.Validate`, `db.ResetSequences`, `dbtest.New`.
- Produces:
  - `db.DataTables(ctx, q db.Querier) ([]string, error)`: the base tables of `current_schema()` except `schema_migrations`, sorted.
  - `importer.Options{DataDir string; Replace bool}`.
  - `importer.Run(ctx context.Context, pool *pgxpool.Pool, src string, opt Options) (map[string]int, error)`. `src` is a bundle directory or a `.tar.gz`. It returns the rows loaded per data file and adds `files` for the files copied. The steps:
    1. Hold `pg_advisory_lock(7212027)` on one dedicated connection for the whole run, so that two imports cannot interleave.
    2. Make the staging directory `DataDir/.import-<16 hex>/`. Extract a `.tar.gz` into its `bundle/`. `Open` the bundle and `Check` it. Any error stops the run here.
    3. Without `Replace`, refuse when any data table has a row, or when any of the five data directories has an entry. The error names the table or the directory.
    4. Copy each file to `staging/files/<path>`, hashing as it copies, and refuse a mismatch. When the bundle came from an archive, rename the files instead of copying them.
    5. On the dedicated connection, begin a transaction. With `Replace`, run `truncate <every data table> restart identity cascade`. Then run `set constraints all deferred`, and load the files in `Files` order. This task loads `ranks`, `users`, `gone_users`, `groups`, `group_members`, `roles`, `grants`, `forums`, `bans`, `smilies`, `legacy_routes`, `legacy_files`, `static_pages` and the settings. Task 6 adds the rest. Then `ResetSequences`, then commit.
    6. With `Replace`, rename each existing data directory to `DataDir/.replaced-<UTC time as 20061023T150405Z>/<dir>`. Then rename each staged directory into `DataDir`, and create the empty ones. Remove the staging directory.

    On any error before the commit, roll back and remove the staging directory. If a rename in step 6 fails, return an error that names the staging directory and says that `--replace` repairs it.
  - Loading rules for this task:
    - Insert IDs explicitly.
    - Store a `setting` as 3, 1 or 2 (`acl.Yes`, `acl.No`, `acl.Never`).
    - Store a grant's `forum_id` as given, so 0 means board-wide.
    - For `users`:
      - `username_clean` and `email_clean` come from `account`.
      - `password_scheme` and `password_hash` come from `password`.
      - `avatar_path` is the file path.
      - Parse the `signature` source with its own flags and the bundle's smiley list. Store the tree as JSON when the source is not empty, and `NULL` when it is empty.
    - Insert static pages with their parsed tree.
    - Store each settings field as one row in `settings`: the key is the JSON name, and the value is its JSON. `header_image` stores the path, or `null`.
    - Validate every parsed tree with `markup.Validate`. An error stops the import and names the row.

- [ ] **Step 1: Write the failing tests** (each uses `dbtest.New` and `t.TempDir()` as `DataDir`)
  - `TestImportPeopleAndPermissions`: import `Small()`. This task's loaders run; Task 6's do not exist yet. Then:
    - the users, groups, members, roles, grants, forums and settings tables hold exactly the bundle's rows, with the bundle's IDs;
    - `username_clean` is cleaned;
    - a user whose signature source is empty has a `NULL` tree;
    - a fresh `insert into users` with no `id` gets the next ID after the largest.
  - `TestImportCopiesFiles`: after the import, each file is at `DataDir/<path>` with its SHA-256, and no `.import-*` directory remains.
  - `TestImportFromArchive`: `Pack` the bundle, import the `.tar.gz`, and get the same counts.
  - `TestImportRefusesNonEmpty`: a second import of the same bundle fails, with an error that names a table. The data directory and the row counts are unchanged.
  - `TestImportRefusesNonEmptyDataDir`: a stray file in `DataDir/avatars/` makes the import fail before any row loads.
  - `TestImportReplaceKeepsOldFiles`: import, then import a changed bundle with `Replace`. The new rows replace the old, and the old avatars sit under `DataDir/.replaced-*/avatars/`.
  - `TestImportStopsOnCheck`: a bundle with a broken reference fails with `Check`'s message, and leaves no rows and no staging directory.
  - `TestImportLockSerializes`: two concurrent imports into one empty database. Exactly one succeeds, and the other fails on the non-empty check after the first commits.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/importer/ ./internal/db/`. Expected: FAIL.
- [ ] **Step 3: Implement.** One loader function per data file, each built on `bundle.Each`. Use plain inserts for small files. Task 6 uses `CopyFrom` for the large ones.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/importer/ ./internal/db/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "importer: frame, files, people, permissions and forums" && git push`

---

### Task 6: The importer: topics, posts and everything that hangs on them

**Files:**
- Create: `internal/importer/content.go`, `internal/importer/counters.go`, `internal/importer/content_test.go`

**Interfaces:**
- Consumes: Task 5's frame, `markup`.
- Produces loaders for `topics`, `posts`, `polls` (with `poll_options`), `poll_votes`, `attachments`, `pms` (with `pm_recipients`), `topic_watches`, `bookmarks`, `topic_marks` and `forum_marks`. It also produces the counters, which run in the transaction after the last loader.
  - `posts` and `pms` load through `CopyFrom` in batches of 1,000 rows. For each post:
    - `forum_id` comes from its topic.
    - Parse the source with the post's flags and the bundle's smiley list, then `Validate` the tree.
    - `body_tree` is the tree's JSON, and `body_text` is `Render(tree, markup.RenderContext{}).Text`.
    - `enable_sig` is `show_signature`.
  - `attachments.stored_path` is `file.path`, and `size` and `sha256` come from the ref.
  - Counters, computed in SQL:
    - A topic's last post is its newest approved post, by `created_at` and then `id`. When the topic has no approved post, it is the newest post of any visibility. `last_post_id` and `last_post_at` come from it.
    - A moved-topic marker has a null `last_post_id`, and its `last_post_at` is its `created_at`.
    - `reply_count` is the number of approved posts minus 1, and never below 0.
    - For a forum, `topic_count` counts its approved non-marker topics. `post_count` counts the approved posts in its approved topics. `last_post_id` is the newest of those posts. Counts do not roll up to parent forums.

- [ ] **Step 1: Write the failing tests**
  - `TestImportSmallBoard`: import `Small()`. Then:
    - every table's row count equals the bundle's;
    - the guest post has a null `author_id` and its `author_name`;
    - the post with an attachment has a tree that holds an `img` with `src` `/a/<id>`;
    - the post with a smiley has a `smiley` node;
    - `body_text` contains the post's plain words;
    - a `plainto_tsquery` search for one word finds the post.
  - `TestImportCounters`: in `Small()`, the topic with one unapproved reply gets `reply_count` 1, and its last post is the newest approved post. The marker has a null `last_post_id`. The forum and subforum counts match a hand count written in the test.
  - `TestImportFlagsRespected`: a post with `markdown` off stores `**x**` as literal text. A post with `autolink` off stores a bare URL as text, not a `url` element.
  - `TestImportIsAllOrNothing`: make post 6's tree fail `Validate`, by replacing `markup.Parse` through an unexported package variable in the test. The import fails, every data table is empty, `DataDir` has no data directories, and no staging directory remains.
  - `TestImportScale`: `bundletest` builds 35,000 posts in 2,500 topics (bodies of about 600 bytes with Markdown), and the import finishes in under 120 s under `-race`. Skip it with `testing.Short()`. CI runs it without `-short`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/importer/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/importer/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "importer: topics, posts, polls, messages and counters" && git push`

---

### Task 7: `gobb import`

**Files:**
- Modify: `cmd/gobb/main.go`, `cmd/gobb/main_test.go`, `README.md`, `AGENTS.md`

**Interfaces:**
- Consumes: `importer.Run`, `config.FromEnv`, the existing `applyMigrations`.
- Produces:
  - `gobb import [--replace] <bundle>`, using `flag.NewFlagSet("import", flag.ContinueOnError)`. It loads the config, applies migrations, then calls `importer.Run` with `DataDir` from the config.
  - On success it prints one line per data file in `Files` order, then a `files` line, each as `%-14s %d`, to stdout, and exits 0.
  - On error it logs the error and exits 1. Without a bundle argument it prints usage to stderr and exits 2.
  - `cmd/gobb` imports `time/tzdata`, so timezone checks work in the static image.
- [ ] **Step 1: Write the failing tests** in `main_test.go`:
  - `TestImportCommand`: run the command's handler with a `bundletest` directory and a test database. The output has a line that matches `^posts +6$`, and a line that starts with `files`.
  - `TestImportCommandUsage`: no arguments gives exit code 2.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./cmd/gobb/`. Expected: FAIL.
- [ ] **Step 3: Implement**, and add an "Import a bundle" section to `README.md`. Drive it from a variable:

  ```sh
  export BUNDLE=/path/to/bundle.tar.gz
  docker compose stop gobb
  docker compose run --rm -v "$BUNDLE:/import/bundle.tar.gz:ro" gobb import /import/bundle.tar.gz
  docker compose start gobb
  ```

  Run these commands against the Compose stack with `docs/example-bundle/` packed by `bundle.Pack`, and correct them until they work. Say that `--replace` moves the old files under `.replaced-*` in the data volume. Add the import command to `AGENTS.md`.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./cmd/gobb/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "cmd: gobb import" && git push`

---

### Task 8: Permissions from the database, and `gobb perms export`

**Files:**
- Create: `internal/acldb/acldb.go`, `internal/acldb/acldb_test.go`
- Modify: `cmd/gobb/main.go`, `cmd/gobb/main_test.go`

**Interfaces:**
- Consumes: `acl.Subject`, `acl.Resolve`, `acl.Options`, `db.Querier`, the importer (to load fixtures in tests).
- Produces:
  - `acldb.Load(ctx context.Context, q db.Querier, userID int64) (acl.Subject, error)`.
    - Grants come from the user's own grants, plus the grants of every group the user belongs to with `is_pending = false`. Role grants are expanded through `acl_role_settings` to one `acl.Grant` per option, on the grant's forum.
    - `Owner` is `users.is_owner`.
    - `userID` 0 is a guest: the grants of the `guests` built-in group only.
    - An unknown user returns an error.
  - `acldb.Export(ctx context.Context, q db.Querier, w io.Writer) error` writes JSON Lines: the guest first, then each user by ID. Each line is `{"user_id":N,"board":[...],"forums":{"<id>":[...],...}}`.
    - `board` lists the Board and Moderator options that `Has(option, 0)` grants, sorted.
    - `forums` has a key for every forum, and lists the Forum and Moderator options that `Has(option, forumID)` grants there, sorted. A forum with none gets `[]`.
  - `gobb perms export` writes the export to stdout. Other `perms` subcommands print usage and exit 2.
- [ ] **Step 1: Write the failing tests**
  - `TestLoadExpandsRolesAndGroups`: in `Small()`, the member of a group with a forum role gets each of the role's options on that forum. A pending member does not get the group's grants.
  - `TestLoadNeverWins`: in `Small()`, the user whose own grant is `never` for `forum.read` on the forum, where a group grants `yes`, gets `Has == false`.
  - `TestLoadGuest`: user 0 gets exactly the `guests` group's grants.
  - `TestExportFormat`: export `Small()`. The first line is user 0, and the lines follow in ID order. A forum the guest cannot read lacks `forum.read`. The output is byte-identical across two runs.
  - `TestPermsCommand`: `gobb perms export` prints the same lines, and `gobb perms` alone exits 2.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/acldb/ ./cmd/gobb/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Load each subject with a few queries. Do not query once per option.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/acldb/ ./cmd/gobb/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "acldb: subjects from the database; gobb perms export" && git push`

---

## Order

- Task 1 and Task 2 can run at the same time, because they touch different files.
- Then Tasks 3 and 4, in order.
- Then Task 5.
- After Task 5, Task 6 and Task 8 can run in parallel. Task 8 touches `cmd/gobb` and `internal/acldb` only.
- Task 7 comes after Task 6, and rebases over Task 8's change to `main.go`.
