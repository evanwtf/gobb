# gobb Plan 3: Read side — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `gobb serve` shows the imported board in a browser: the layout and the three Slate schemes, the board index, forums, topics, sign-in, "new since your last visit", profiles, the member list, the team page, search, old URLs, and the security headers. It builds the board one visible slice at a time.

**Architecture:** `internal/board` holds the read queries. Each query takes a `Viewer`, whose permissions come from `acldb.Load`, and returns plain structs with nothing the viewer may not see. `internal/web` turns those structs into pages with embedded `html/template` files, one CSS file and one small JS file. It uses the standard `http.ServeMux`, mounted under the base path. `internal/session` and `internal/login` own sign-in, and `internal/legacy` matches old URLs. Pages render each post on read, from its stored `body_tree`, with `markup.Render`.

**Tech Stack:** Go 1.27 standard library (`net/http` method and wildcard patterns, `http.CrossOriginProtection`, `html/template`, `embed`, `os.Root`, `crypto/hmac`); pgx v5; PostgreSQL full-text search. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md` (sections 3, 7, 9, 10 "Login" and "Sessions", 12, 13, 14, 15, 19). Roadmap: `docs/superpowers/plans/2026-10-23-gobb-roadmap.md`, Plan 3's row and both "Carried" sections. Format of stored data: `docs/bundle.md`.

**Workers:** Sonnet, high effort. Reviewer: Opus, high effort, after every task and over the whole plan at the end.

## Global Constraints

- **Clean room.** Work only from this repo and public standards (RFCs, the Go, PostgreSQL and WCAG docs, and each embed provider's published embed documentation). Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. Write the HTML, CSS and JS from scratch. Name no other forum software in code, docs, tests or commits. If a task needs a fact about another system, stop and ask. (`AGENTS.md`; spec 1.)
- **Real data stays with the controller.** At each checkpoint the controller runs `gobb serve` against a local, private copy of a real board that it imported with `gobb import`. Workers never see that data: no database access, no screenshots, no text copied from it. Workers build and test with `bundletest` fixtures and made-up data only. When the owner's feedback cites real content, the controller restates it with made-up names and text before a worker sees it.
- This repo is public. Never commit hostnames, IP addresses other than `127.0.0.1`, `10.x` and the IPv6 documentation prefix `2001:db8::/32`, home-directory paths, secret values, or any forum data.
- Module `github.com/evanwtf/gobb`; the latest Go 1.27.x. No new dependencies (spec 15). A small helper (a weighted semaphore, a contrast formula) is written in the repo.
- Commit subjects: `area: imperative summary`. No agent session URLs or IDs, no agent trailers. Work lands on `main`; push after every task. Each task adds its bullet to `docs/changelog.md` under `## 0.1.0 (unreleased)`.
- Tests that need PostgreSQL use `dbtest.New` (through `boardtest.Load`) and skip when `GOBB_TEST_DATABASE_URL` is unset. CI sets it and fails on any skip.
- Logs go through `log/slog` to stdout.
- **Migrations.** Never edit `0001_schema.sql` or a migration that an earlier task added. The controller's checkpoint copy has applied them, and `db.Migrate` refuses a changed file. A task that needs schema adds the next numbered file.
- **GET and HEAD change nothing**, except the read bookkeeping that Task 4 defines: the session's last-seen time, the member's visit times, topic read marks and topic view counts. Topic marks and view counts change only on a top-level navigation. Every POST passes `http.CrossOriginProtection` and checks the CSRF token (spec 10).
- **Permission before redirect.** Spec 14, verbatim: "The permission check runs before any redirect. A target the visitor cannot see gets the same 404 as a missing one, so a redirect never reveals a hidden topic."
- No Plan 3 code inserts users or resets the `users` identity, so a gone user's ID stays reserved (Plan 2 carried).
- **Names.** Every member name in HTML sits in `<bdi>`. A name with no user ID (a guest post, a gone author) shows its `author_name` with no profile link.
- **Paths.** Every `href`, `src` and `action` that gobb writes is a site path that starts with the base path, an `https:` or `mailto:` URL, or a `#` anchor. Handlers build site paths with one helper, `site.path`.
- **No inline script, no `style` attributes and no `<style>` elements** in templates. The CSP of Task 9 forbids them, and a test there scans the templates.
- **Times.** Guests see times in the board's `timezone` setting; members in their own `users.timezone`. Format: Go layout `Jan 2, 2006, 3:04 pm`, inside `<time datetime="…">` with the RFC 3339 time in UTC. Dates alone use `Jan 2, 2006`.
- **Page sizes:** 50 topics, 25 posts, 50 members, 25 search results.
- **Checkpoints.** Each task ends with a `Checkpoint:` line: what the owner sees when the task lands. The controller runs it before the next task starts. Feedback from the owner becomes a fix commit, or a note on a later task, before work goes on.

## Review Focus

1. **A hidden forum leaking through a side door.** A guest or member who lacks `forum.read` on a forum must not see its topic titles, post text or attachments through any path: `/t/`, `/p/`, `/a/`, the last-post column, subforum totals, global topics, "new since your last visit", search, or a legacy redirect. Test: `TestHiddenForumNeverLeaks` (Task 7).
2. **A post image that points at a site path.** `![x](/t/1)` or `![x](/new)` makes every reader's browser send a GET with their cookies. That GET must change nothing that matters. Test: `TestGetHasNoSideEffects` (Task 4; later tasks add their paths to it).
3. **A base path other than `/`.** With `GOBB_BASE_PATH=/forum/`, one link without the prefix sends a member off the board. Test: `TestBasePathEverywhere` (Task 2; later tasks add their pages to it).
4. **Odd page numbers.** `?page=0`, `-1`, `abc`, a 30-digit number, or a page past the end, from crawlers and old links. Each gets 400 or 404, never a 500 or an empty 200. Test: `TestPaginationBounds` (Task 2).
5. **Many sign-ins at once on a 1 GB host.** A stored argon2id hash may need up to 256 MiB. Twenty logins at once must not run twenty hashes at once. Test: `TestLoginMemoryBudget` (Task 8).

---

## Decisions this plan fixes

**URLs.** All are under the base path. Plan 2 fixed the first five.

| Path | Page |
|---|---|
| `/` | board index |
| `/f/{id}?page=N` | forum or category; a link forum answers 302 to its `link_url` |
| `/t/{id}?page=N` | topic; a moved-topic marker answers 301 to its target |
| `/p/{id}` | 302 to `/t/{topic}?page=N#p{id}`, with the page as this viewer sees it |
| `/u/{id}` | profile; 410 for a gone user |
| `/a/{id}` | attachment file |
| `/t/{id}/new` | 302 to the viewer's first unread post |
| `/new` | new since your last visit |
| `/members`, `/team`, `/search`, `/pages/{slug}` | member list, team, search, static page |
| `/login`, `/logout`, `/scheme`, `/read`, `/f/{id}/read` | sign-in and the POST actions |
| `/account` | 302: to `/login?next=…` for a guest, to the member's own profile otherwise (Plan 4 builds the page) |
| `/files/{dir}/{name}` | `avatars`, `smilies`, `ranks` and `header` only |
| `/static/{name}` | CSS and JS |
| `/healthz` | always at the host root, whatever the base path |

Page 1 has no `?page=` in the links gobb writes.

**Files.** `/files/` serves four of the five data directories. Attachments are served only through `/a/{id}`, because the download rights apply to them. Nothing serves the `.import-*` or `.replaced-*` folders.

**Who sees which posts.** An approved post or topic: everyone who can read the forum. An unapproved one: its author, and viewers with `mod.approve` in the forum. A deleted one: viewers with `mod.delete` in the forum. The page marks the last two kinds. Counters and the last-post column count approved posts only, as Plan 2 computes them.

**Unread.** A topic is unread for a member when its `last_post_at` is after the newest of: `users.last_mark_at` (or `registered_at` when that is null), the member's `forum_marks` row for the topic's forum, and the member's `topic_marks` row for the topic. Guests never see unread marks.

**Visits.** Spec 10 says "Last visit updates when a new session starts". A "remember me" session lasts a year, so a visit starts instead when a member's request comes more than 30 minutes after their previous one, and at sign-in. Then `last_visit_at` takes the old `last_active_at` (or keeps its imported value when that is null), and `last_active_at` takes the current time. Within a visit, `last_active_at` moves at most once every 5 minutes.

---

### Task 1: The layout, the Slate schemes and the board index

**Files:**
- Create: `internal/board/board.go` (`Settings`, `Viewer`, errors), `internal/board/forums.go`, `internal/board/forums_test.go`
- Create: `internal/boardtest/boardtest.go`
- Create: `internal/web/site.go`, `internal/web/pages.go`, `internal/web/files.go`, `internal/web/templates/layout.html`, `internal/web/templates/index.html`, `internal/web/templates/error.html`, `internal/web/static/gobb.css`, `internal/web/static/gobb.js`, `internal/web/helpers_test.go`, `internal/web/index_test.go`, `internal/web/files_test.go`, `internal/web/scheme_test.go`
- Create: `internal/db/migrations/0002_read_indexes.sql`
- Modify: `cmd/gobb/main.go`, `cmd/gobb/main_test.go`, `README.md`, `AGENTS.md`, `docs/changelog.md`

**Interfaces:**
- Consumes: `acldb.Load`, `acl.Set`, `config.Config`, `importer.Run`, `bundletest.Small`, `bundletest.Write`, `dbtest.New`, `web.Health`.
- Produces:
  - `boardtest.Load(t testing.TB, b *bundletest.Board) (pool *pgxpool.Pool, dataDir string)`: a test database with `b` imported, and the data directory it used.
  - `board.Settings{BoardName, BoardDescription, Timezone, DefaultScheme, Registration string; EmailNotifications bool; HeaderImage string}` and `board.LoadSettings(ctx context.Context, q db.Querier) (Settings, error)`. It reads the seven keys that Plan 2 stores (`board_name`, `board_description`, `timezone`, `default_color_scheme`, `registration`, `email_notifications`, `header_image`). A missing key is an error that names it. `HeaderImage` is `""` when the value is `null`.
  - `board.Viewer{UserID int64; Name string; Perms acl.Set; Loc *time.Location; Scheme string}`. `UserID` 0 is a guest. `board.Guest(ctx, q, s Settings) (Viewer, error)` loads the guests' grants with `acldb.Load(ctx, q, 0)`.
  - `board.ErrNotFound`, `board.ErrForbidden`, `board.ErrGone`.
  - `board.Author{ID int64; Name string}` (ID 0: no profile link). `board.PostRef{ID, TopicID int64; TopicTitle string; Author Author; At time.Time}`.
  - `board.Forum{ID, ParentID int64; Kind, Name, Description, Rules, LinkURL string; Locked bool; Topics, Posts int; Last *PostRef; Children []*Forum}`.
  - `board.Index(ctx, q, v Viewer) ([]*Forum, error)`. It returns the top-level forums that `v` may list, each with its listed children, in `position` then `id` order. A forum is listed when it and every ancestor have `forum.list`. `Topics`, `Posts` and `Last` add up the forum and every descendant that `v` may read, so a hidden subforum adds nothing.
  - `web.Options{BasePath, DataDir string; TrustedProxies []netip.Prefix; CookieSecure bool; Now func() time.Time}`.
  - `web.New(ctx context.Context, pool *pgxpool.Pool, opt Options) (http.Handler, error)`. The handler serves `/healthz` at the host root and the site under `BasePath`. Other paths get 404.
  - The unexported `site` type holds the pool, the options and the parsed templates. `site.path(parts ...string) string` joins the base path and the parts.
  - Routes: `GET /` (index), `GET /static/{name}`, `GET /files/{dir}/{name...}`.
  - `cmd/gobb`: `newHTTPServer(h http.Handler) *http.Server`, and `serveUntil(ctx context.Context, srv *http.Server, ln net.Listener, closePool func(), sigs <-chan os.Signal, exit func(int)) error`.

The look (spec 13), written from scratch:
- A header band in `--header` with the header image (`/files/<header_image>`), or the board name as text when there is none. Under it, a nav bar. In this task it holds Index only; later tasks add their links.
- Category bars in `--bar`. Under each bar, a table of forum rows in `--row`, with four columns: the forum (name, description, and a "Subforums:" line of links), Topics, Posts, and Last post (the topic title linked to `/p/{id}`, "by" the author, and the time).
- Forums with no category above them sit under a bar with no title. A link forum shows its link and no counts.
- System UI fonts; no web fonts. Below 700 px wide, the four columns stack.
- `gobb.js` toggles the `shown` class on a `.spoiler` on click or Enter. Without JS, `.spoiler:focus` shows the text.
- `<html data-scheme="…">` picks the scheme. `?scheme=slate|slate-deep|slate-light` shows one page in that scheme and stores nothing. Otherwise the page uses the board default.

The schemes in `gobb.css`, one rule `:root[data-scheme="<name>"]` each, all with the same tokens: `--page`, `--row`, `--bar`, `--bar-text`, `--header`, `--header-text`, `--text`, `--muted`, `--link`, `--accent`, `--new`.
- **Slate**, from spec 13: `--page #233346`, `--row #2a3c52`, `--bar #596d86`, `--link #4fc0ff`, `--text #dbe3ec`, `--header #2e425b`, `--accent #009aff`, `--new #ff0000`.
- **Slate Deep**: a near-black navy page and rows, with `--bar #2e425b`.
- **Slate Light**: a light page and white rows, `--bar #2e425b`, `--header #2e425b` and `--accent #009aff`. Links use a darker azure, because `#009aff` fails WCAG AA on white.
- Choose the other values. The test below holds them to AA. The `--new` badge is outside that rule: spec 13 asks AA for body text and links.

`serve`:
- `http.Server` limits: `ReadHeaderTimeout` 10 s, `ReadTimeout` 30 s, `WriteTimeout` 120 s, `IdleTimeout` 120 s, `MaxHeaderBytes` 64 KiB.
- On the first SIGTERM or SIGINT, it calls `Shutdown`, then `closePool`, all inside the 25 s window. If `closePool` has not returned when the window ends, `serveUntil` returns an error that says so.
- A second signal during shutdown calls `exit(1)` at once.

`/files/`:
- Only the four named directories. Open each through an `os.Root` on `DataDir/<dir>`.
- Refuse any name with an element that starts with `.`, and anything that is not a regular file.
- Sniff the first 512 bytes with `http.DetectContentType`. Serve only `image/png`, `image/jpeg`, `image/gif` and `image/webp`, with that type, `X-Content-Type-Options: nosniff` and `Cache-Control: public, max-age=86400`. Anything else gets 404.

`0002_read_indexes.sql`: an index on `topics (forum_id, last_post_at desc, id desc)` and one on `posts (author_id)`.

- [ ] **Step 1: Write the failing tests**
  - `TestLoadSettings` (board): after `boardtest.Load(t, Small())`, `BoardName` is `Example Board`, `Timezone` `America/New_York`, `HeaderImage` `header/banner.png`. With the `board_name` row deleted, the error names `board_name`.
  - `TestIndexGuest` (board): the guest gets one top-level forum, `General` (1), with one child, `Main Discussion` (2). Forum 2 has no children for the guest. Its `Topics` is 1 and `Posts` is 3, and `Last` is post 4 by `Visitor` with author ID 0.
  - `TestIndexRollsUpReadableOnly` (board): a viewer built from user 1's grants (alice, in `admins`) sees forum 3 as a child of forum 2. Forum 2 then shows `Topics` 2 and `Posts` 5, and `Last` is still post 4, the newest.
  - `TestIndexPage` (web): `GET /` answers 200 with `<html data-scheme="slate"`, `Example Board`, `src="/files/header/banner.png"`, `General` and `Main Discussion`. The body has no `Members Lounge`. The guest post's author shows as `<bdi>Visitor</bdi>`, with no `/u/` link.
  - `TestSchemePreview` (web): `?scheme=slate-light` gives `data-scheme="slate-light"`; `?scheme=evil` gives the default.
  - `TestSchemesPassAA` (web): parse the three scheme rules from the embedded `gobb.css`. Each scheme defines every token. Slate's values equal the spec values above. The WCAG 2 contrast ratio is at least 4.5 for each pair: `--text` and `--muted` on `--page` and on `--row`; `--link` on `--page` and on `--row`; `--bar-text` on `--bar`; `--header-text` on `--header`.
  - `TestFiles` (web):
    - `/files/avatars/1.png` answers 200, `image/png`, `nosniff`, with the fixture's bytes.
    - These answer 404: `/files/attachments/1.png`; `/files/avatars/../avatars/1.png`; `/files/.import-x/a.png`; a file `avatars/.hidden.png`; a symlink in `avatars/` to a file outside it; a file `avatars/x.png` that holds `<script>`.
  - `TestNotFound` (web): `/nope` answers 404 with the layout and the text `Page not found`.
  - `TestServerLimits` (cmd): the fields of `newHTTPServer(h)` equal the values above.
  - `TestServeUntil` (cmd):
    - A cancelled context returns nil, after `closePool` ran.
    - A `closePool` that blocks returns an error once the window ends. The test sets the window to 100 ms through a package variable.
    - A second signal during a slow shutdown calls `exit(1)`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/board/ ./internal/web/ ./cmd/gobb/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement.** Parse the templates once in `web.New`, from an `embed.FS`, with a `FuncMap` for `path`, `time` and `date`. The partial `{{template "name" .}}` takes an `Author`. It writes `<a href="…/u/ID"><bdi>NAME</bdi></a>`, or `<bdi>NAME</bdi>` alone when ID is 0. `board.Index` reads all forums in one query, and the last posts of the forums it shows in one more. In `serve`, build the handler with `web.New`, and replace the old `defer pool.Close()` with `serveUntil`. Add a "Run the server" section to `README.md`, driven from variables, and add `./gobb serve` to the `AGENTS.md` command list.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/ ./internal/web/ ./cmd/gobb/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: layout, Slate schemes and the board index" && git push`

**Checkpoint:** `gobb serve` shows the header image, the category bars and the forum rows that guests may see, with counts and last posts, in Slate. `?scheme=slate-deep` and `?scheme=slate-light` show the other two schemes, and the page works on a phone.

---

### Task 2: Forum and topic pages

**Files:**
- Create: `internal/board/topics.go`, `internal/board/posts.go`, `internal/board/topics_test.go`, `internal/board/posts_test.go`
- Create: `internal/web/forum.go`, `internal/web/topic.go`, `internal/web/templates/forum.html`, `internal/web/templates/topic.html`, `internal/web/forum_test.go`, `internal/web/topic_test.go`, `internal/web/basepath_test.go`
- Modify: `markup/render.go`, `markup/render_test.go`, `internal/web/site.go`, `internal/web/static/gobb.css`, `docs/changelog.md`

**Interfaces:**
- Consumes: Task 1; `markup.Decode`, `markup.Render`, `markup.Smiley`.
- Produces:
  - `markup.RenderContext.BasePath string`. When it is set (for example `/forum/`), the renderer puts it, minus its last `/`, in front of every site path it writes: a `url` href, an `img` src and a smiley image. It leaves `http`, `https` and `mailto` targets alone. `Rendered.Text` does not change.
  - `board.TopicRow{ID int64; Title, Kind, Visibility string; Locked bool; Author Author; Replies, Views int; Last PostRef; MovedTo int64}`.
  - `board.ForumPage{Forum *Forum; Crumbs []*Forum; Subforums []*Forum; Pinned, Topics []TopicRow; Page, Pages int}` and `board.ForumView(ctx, q, v Viewer, forumID int64, page int) (ForumPage, error)`.
  - `board.UserCard{ID int64; Name, AvatarPath, RankTitle, RankImage string; PostCount int; Joined time.Time}`.
  - `board.Attachment{ID int64; Name, Mime, Comment string; Size int64; Downloads int; Inline bool}`.
  - `board.Post{ID int64; Author Author; Card *UserCard; At time.Time; Subject, Visibility string; Tree, Signature *markup.Node; EditCount int; EditedAt *time.Time; EditedBy *Author; EditReason, IP string; Attachments []Attachment}`. `IP` is empty unless the viewer has `mod.view_ip` in the forum.
  - `board.Poll{Question string; MaxOptions int; EndsAt *time.Time; Options []PollOption; Total int}` and `board.PollOption{ID int; Text string; Votes int}`.
  - `board.TopicPage{Topic TopicRow; Forum *Forum; Crumbs []*Forum; Posts []Post; Poll *Poll; Page, Pages int}` and `board.TopicView(ctx, q, v, topicID int64, page int) (TopicPage, error)`.
  - `board.MovedError{To int64}` (from `TopicView` for a marker) and `board.LinkError{URL string}` (from `ForumView` for a link forum).
  - `board.PostLocation(ctx, q, v, postID int64) (topicID int64, page int, err error)`.
  - `board.AttachmentMeta{Path, Mime, Name string; Size int64}` and `board.AttachmentFile(ctx, q, v, id int64) (AttachmentMeta, error)`.
  - `board.SmileyLookup(ctx, q) (func(code string) (markup.Smiley, bool), error)`. The image of a smiley is the site path `/files/<image_path>`, and `Alt` is its `description`. The renderer adds the base path.
  - `board.ParsePage(s string) (int, error)`: `""` gives 1. A value that is not a decimal integer from 1 to 1,000,000 is an error.
  - Routes: `GET /f/{id}`, `GET /t/{id}`, `GET /p/{id}`, `GET /a/{id}`.

Rules:
- **Forum page.**
  - A forum that `v` may read answers 200. One that `v` may list but not read answers 403, with a sign-in link for a guest. Any other answers 404.
  - A category shows its child forums, as the index does.
  - A forum shows its rules and its subforums, then the topic table.
  - `Pinned` holds the forum's `announce` topics, plus every `global` topic in a forum that `v` may read. They show on every page and do not count toward pages.
  - `Topics` is in order: sticky first, then `last_post_at` descending, then `id` descending.
  - A moved-topic marker shows as "Moved:" and links to its target. It shows only when `v` may read the target.
- **Topic page.**
  - It needs `forum.read` on the topic's forum, or it answers 404.
  - Posts are in `created_at` then `id` order.
  - The author block shows the avatar, the name, the rank (title and image) and the post count. A guest post shows its `author_name` and the word "Guest".
  - The rank is `users.rank_id` when set. Otherwise it is the rank with the largest `min_posts` at or below the user's `post_count` among ranks with `special = false`.
  - The signature shows when the post's `enable_sig` is true and the author has a `signature_tree`.
  - The edit note says who edited the post, when, how many times, and the reason.
  - A poll shows its question, each option's count with a bar, and the total. Voting is Plan 4.
  - Each post is `<article id="p{id}">`.
  - The page renders each `Tree` with `markup.Render`, with `Smiley` from `SmileyLookup` and `BasePath` from the options.
- **Attachments.** The list under a post holds the attachments that its tree does not show inline. Inline means an `img` whose `src` is `/a/{id}`.
- **`/a/{id}`.**
  - It answers 404 when the post's forum is not readable, when the post is not visible to `v`, or when the attachment has no post and `v` is neither its uploader nor an admin.
  - It answers 403 when `v` lacks `account.download` or the forum's `attach.download`.
  - Otherwise it serves the file with the stored `mime`, `nosniff`, `Cache-Control: private, max-age=3600`, and `Content-Disposition: inline; filename*=UTF-8''<percent-encoded name>`.
  - It does not change `download_count`.
- **Pages.** A bad `?page=` answers 400. A page after the last answers 404. An empty forum has one page.

- [ ] **Step 1: Write the failing tests**
  - `TestRenderBasePath` (markup): with `BasePath` `/forum/`, `[x](/t/5)` gives `href="/forum/t/5"`, `![d](/a/1)` gives `src="/forum/a/1"`, and a smiley with image `/files/smilies/smile.gif` gives `src="/forum/files/smilies/smile.gif"`. `https://a.example/x` and `mailto:a@b.example` do not change. `Text` equals the `Text` without `BasePath`.
  - `TestForumViewGuest` (board):
    - forum 2 gives topic 1 in `Topics`, kind `sticky`, `Replies` 2 (the approved posts minus 1);
    - forum 3 gives `ErrNotFound`;
    - forum 1 gives forum 2 among `Subforums`.
  - `TestForumViewMarkerAndGlobal` (board): grant guests `forum.list` and `forum.read` on forum 3. Then forum 3 shows the marker with `MovedTo` 1. With topic 2's kind set to `global`, topic 2 is in `Pinned` on forum 2.
  - `TestTopicViewGuest` (board): topic 1 gives posts 1, 2 and 4, not post 3. Post 1 has a `Card` with rank `Newcomer` and post count 120, a signature, `EditCount` 1 and reason `typo`. Post 2 has no `Attachments`, because attachment 1 is inline. Post 4 has author ID 0 and name `Visitor`. `IP` is empty everywhere.
  - `TestTopicViewPoll` (board): with guests granted read on forum 3, topic 2 has a poll with Green 3, Blue 2, Red 0, total 5.
  - `TestTopicViewMarker` (board): topic 3 gives `MovedError{To: 1}`.
  - `TestTopicPage` (web):
    - `/t/1` has `<strong>example</strong>` and `id="p1"`, and `src="/a/1"` inside post 2.
    - It has the signature text `Example signature`.
    - Post 4's author is `<bdi>Visitor</bdi>`, with no profile link before it.
    - Post 1's time reads `May 2, 2019, 5:00 am`.
  - `TestTopicSmiley` (web): with guests granted read on forum 3, `/t/2` has `<img class="smiley" src="/files/smilies/smile.gif"`.
  - `TestRedirects` (web):
    - `/t/3` answers 301 to `/t/1`;
    - `/p/4` answers 302 to `/t/1#p4`;
    - `/p/3` (unapproved), `/p/5` (forum 3) and `/p/99` answer 404;
    - a link forum answers 302 to its URL.
  - `TestAttachmentRights` (web):
    - as a guest, `/a/1` answers 403 and `/a/2` (no post) 404;
    - with `account.download` granted to guests board-wide and `attach.download` on forum 2, `/a/1` answers 200, `image/png`, `nosniff`, with the fixture's bytes;
    - `download_count` stays 7.
  - `TestNamesInBdi` (web): with alice renamed `al` + U+202E + `ice`, each occurrence of that name in `/` and `/t/1` sits directly inside `<bdi>…</bdi>`.
  - `TestPaginationBounds` (web): load `bundletest.Large(3, 60)`. Topic 1 has 60 posts.
    - `/t/1?page=3` answers 200 with 10 articles.
    - `?page=4` answers 404.
    - `?page=0`, `?page=-1`, `?page=abc` and `?page=` followed by 30 nines answer 400.
    - `/p/30` answers 302 to `/t/1?page=2#p30`.
  - `TestBasePathEverywhere` (web): with `BasePath` `/forum/`, fetch `/forum/`, `/forum/f/2` and `/forum/t/1`. Every `href`, `src` and `action` value starts with `/forum/`, `https:`, `mailto:` or `#`. `/t/1` without the prefix answers 404. Later tasks add their pages to this list.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./markup/ ./internal/board/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Build the visibility rule as one SQL clause helper in `internal/board`. It takes the IDs of the forums where `v` may read, approve and delete, computed in Go from `v.Perms` and passed as `bigint[]` arguments. A topic page uses a fixed number of queries, whatever its post count: posts, cards, signatures, attachments and the poll each take one query.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./markup/ ./internal/board/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: forum and topic pages" && git push`

**Checkpoint:** a guest clicks into a forum to see its topic list across pages. Then they open a topic to see its posts with author blocks, avatars, ranks, signatures, smilies, inline images, YouTube players and poll results. A members-only forum stays out of sight. Profile links answer 404 until Task 5, and X, Instagram, Reddit and Threads posts show as plain links until Task 9.

---

### Task 3: Sign in and sign out

**Files:**
- Create: `internal/session/session.go`, `internal/session/session_test.go`
- Create: `internal/login/login.go`, `internal/login/login_test.go`
- Create: `internal/web/auth.go`, `internal/web/clientip.go`, `internal/web/csrf.go`, `internal/web/templates/login.html`, `internal/web/auth_test.go`, `internal/web/clientip_test.go`, `internal/web/member_test.go`
- Create: `internal/db/migrations/0003_sessions.sql`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`, `internal/board/board.go`, `internal/web/site.go`, `internal/web/templates/layout.html`, `cmd/gobb/main.go`, `.env.example`, `compose.yaml`, `README.md`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–2; `password.Verify`, `password.Hash`, `account.CleanUsername`, `account.CleanEmail`, `acldb.Load`.
- Produces:
  - `config.Config.CookieSecure bool`, from `GOBB_COOKIE_SECURE`: `true` (the default) or `false`. Any other value is an error that names the variable. Compose passes it through, and `.env.example` lists it.
  - `web.ClientIP(r *http.Request, trusted []netip.Prefix) netip.Addr`. When `RemoteAddr` is in `trusted`, it walks `X-Forwarded-For` from the right, skips trusted hops, and returns the first address that is not trusted. A bad entry ends the walk and gives `RemoteAddr`. An IPv4-mapped address is unmapped.
  - `session.Session{ID string; UserID int64; CreatedAt, LastSeenAt, ExpiresAt time.Time; Remember bool}`. `ID` is the hex SHA-256 of the token, as in the schema.
  - `session.Create(ctx, q db.Querier, userID int64, remember bool, ip netip.Addr, userAgent string, now time.Time) (token string, s Session, err error)`. The token is 32 random bytes, base64url without padding. It stores the user agent cut to 512 bytes. It upserts `user_ips (user_id, ip, last_seen_at)`.
  - `session.Lookup(ctx, q, token string, now time.Time) (Session, error)`. It returns `session.ErrNone` for an unknown or expired token.
  - `session.Touch(ctx, q, s Session, now time.Time) error`. When `LastSeenAt` is more than 5 minutes old, it sets `last_seen_at` and slides `expires_at`.
  - `session.Delete(ctx, q, token string) error`.
  - Lifetimes: "remember me" 365 days; otherwise 24 hours. Both slide on `Touch`.
  - `session.CSRFToken(secret string) string`: base64url of HMAC-SHA256, keyed with `secret`, over the text `gobb csrf v1`. `session.CheckCSRF(secret, got string) bool` compares in constant time.
  - `login.Service`, `login.New(q db.Querier) *Service`, and `(*Service).Check(ctx, name, pw string, ip netip.Addr, now time.Time) (userID int64, err error)`. The errors are `login.ErrWrong`, `login.ErrAmbiguous`, `login.ErrInactive` and `*login.BannedError{Reason string}`.
  - `board.Member(ctx, q, userID int64, s Settings) (Viewer, error)`.
  - Routes: `GET /login`, `POST /login`, `POST /logout`, `POST /scheme`.
  - `0003_sessions.sql`: indexes on `sessions (user_id)` and `sessions (expires_at)`.

`login.Check`, in order:
1. A `name` that is not valid UTF-8 gives `ErrWrong`, before `CleanUsername` runs.
2. Find the user whose `username_clean` equals `CleanUsername(name)`. If there is none, find the users whose `email_clean` equals `CleanEmail(name)`.
3. If no user matches, give `ErrWrong`.
4. With one user, verify the password. With several (a shared email), verify against each. If any matches, give `ErrAmbiguous`; if none matches, give `ErrWrong`. A login by email never picks one account.
5. A wrong password gives `ErrWrong`. An empty stored hash never matches.
6. Only after a match: a `state` of `inactive` gives `ErrInactive`.
7. A ban that has not ended gives `BannedError` with its `reason_shown`. The bans are a `user` ban on the ID, an `email` ban where `CleanEmail(bans.email)` equals the user's `email_clean`, and an `ip` ban whose prefix holds `ip`.
8. When `Verify` says `needsRehash`, store `password.Hash(pw)` with scheme `gobb`, with `where id = $1 and password_hash = $old`.

Pages:
- `/login` has a form with the name (username or email), the password, "Remember me", `next` and `csrf`.
- `ErrWrong` shows `The name or password is wrong.` `ErrAmbiguous` shows `This email address belongs to more than one account. Sign in with your username.` Both answer 200.
- `ErrInactive` and `BannedError` answer 403, with their message.
- Success creates a session, sets the cookie, and answers 303 to `next`.
- `next` must be a site path under the base path that does not start with `//`. Otherwise gobb uses the index.

The cookie and CSRF:
- The cookie is `gobb_session`: `HttpOnly`, `SameSite=Lax`, `Path` the base path, and `Secure` unless `CookieSecure` is false. "Remember me" adds `Max-Age` for 365 days; without it, the cookie ends with the browser.
- A guest gets a `gobb_csrf` cookie with 32 random bytes, with the same attributes, when a page with a form renders and the cookie is missing.
- The CSRF secret of a request is the session token when one is valid, and the `gobb_csrf` value otherwise. Every form carries `csrf`.
- Every POST runs inside `http.NewCrossOriginProtection().Handler` and needs `CheckCSRF`. A failure answers 403.

Members:
- A request with a valid session gets `board.Member`: the member's grants from `acldb.Load`, the member's time zone and scheme, and `Touch`.
- The nav shows "Signed in as `<bdi>name</bdi>`" and a sign-out button, a POST form.
- `POST /scheme` takes `slate`, `slate-deep` or `slate-light`. For a member it updates `users.color_scheme`. For a guest it sets a `gobb_scheme` cookie for 365 days. Then it answers 303 to `next`.
- The footer has the scheme picker.
- A guest's scheme comes from `?scheme=`, then the cookie, then the board default.

- [ ] **Step 1: Write the failing tests**
  - `TestFromEnvCookieSecure` (config): unset gives true; `false` gives false; `nope` gives an error that names `GOBB_COOKIE_SECURE`.
  - `TestClientIP` (web). Trusted proxies are `10.231.0.1/32` and `10.231.0.2/32`.
    - From `10.231.0.1` with `X-Forwarded-For: 10.9.9.9, 10.1.1.1, 10.231.0.2`, the result is `10.1.1.1`.
    - From `10.7.7.7`, which is not trusted, with any header, the result is `10.7.7.7`.
    - From a trusted proxy with the entry `garbage`, the result is the proxy's address.
    - `::ffff:10.1.1.1` gives `10.1.1.1`.
  - `TestSessionLifecycle` (session): `Create`, then `Lookup` finds the session, and the stored ID is the SHA-256 of the token. `Lookup` after `expires_at` gives `ErrNone`. `Touch` at +1 minute changes nothing. `Touch` at +10 minutes moves `last_seen_at`. `Delete`, then `Lookup` gives `ErrNone`. The `user_ips` row exists.
  - `TestCSRF` (session): a token checks against its own secret, and fails against another secret, an empty string and a cut token.
  - `TestCheck` (login), each a case on `Small()`:
    - alice by name, and by `ALICE@example.com`, gives 1;
    - bob (legacy bcrypt) gives 2, after which `password_scheme` is `gobb` and `password.Verify(…, "gobb")` holds;
    - a wrong password, the unknown name `nobody`, and dave (inactive, empty hash) each give `ErrWrong`;
    - the name `"\xff"` gives `ErrWrong`;
    - with carol's email set to `bob@example.com`: `bob@example.com` with bob's password gives `ErrAmbiguous`, and with a wrong password `ErrWrong`;
    - an email ban on ` Carol@Example.COM` gives carol a `BannedError`;
    - alice, from `10.99.1.2`, gets a `BannedError` from the ip ban `10.99.0.0/16`, with reason `Proxies are not allowed`;
    - dave's ended user ban changes nothing.
  - `TestSignInFlow` (web), with `CookieSecure` true:
    - `GET /login`, then `POST /login` as alice, gives 303 and a `gobb_session` cookie with `HttpOnly`, `SameSite=Lax`, `Secure` and `Path=/`. "Remember me" adds `Max-Age=31536000`.
    - The next `GET /` shows `Signed in as` and `<bdi>alice</bdi>`.
    - `POST /logout` deletes the session row and expires the cookie.
  - `TestPostNeedsCSRF` (web): `POST /login` without `csrf`, with a wrong `csrf`, and with `Origin: https://evil.example` and `Sec-Fetch-Site: cross-site` each answer 403.
  - `TestMemberViews` (web):
    - alice reads `/f/3` and `/t/2`;
    - bob, who has `mod.approve`, sees post 3 on `/t/1` marked `Waiting for approval`;
    - carol, its author, sees post 3 marked the same way;
    - a guest does not see post 3;
    - alice reads `/a/2` (no post), and bob gets 404.
  - `TestMembersDoNotInheritGuests` (web): remove carol from `members`. A guest reads `/t/1`, and carol gets 404 (Plan 2 carried; spec 9.1).
  - `TestMemberTimeAndScheme` (web): bob (Europe/London, `slate-deep`) sees post 1 at `May 2, 2019, 10:00 am`, in `data-scheme="slate-deep"`.
  - `TestViewIP` (web): with `mod.view_ip` granted to `moderators` board-wide, bob sees `10.0.0.5` on `/t/1`, and a guest does not.
  - `TestSchemePicker` (web): a guest's `POST /scheme` with `slate-light` sets `gobb_scheme`, and the next page uses it. bob's sets `users.color_scheme`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/config/ ./internal/session/ ./internal/login/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Add a `signIn(t, h, name, password string) []*http.Cookie` helper to `helpers_test.go` for later tasks. Add `/login` to `TestBasePathEverywhere`. Document `GOBB_COOKIE_SECURE=false` in the README, for a local run over plain HTTP.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/config/ ./internal/session/ ./internal/login/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: sign in and out, sessions and CSRF" && git push`

**Checkpoint:** the owner signs in with an imported member's current password, legacy or gobb. They see the forums that member may read, in that member's time zone and color scheme. The footer picker changes the scheme, and the owner signs out.

---

### Task 4: New since your last visit

**Files:**
- Create: `internal/board/unread.go`, `internal/board/unread_test.go`
- Create: `internal/web/new.go`, `internal/web/templates/new.html`, `internal/web/new_test.go`, `internal/web/sideeffect_test.go`
- Create: `internal/db/migrations/0004_visits.sql`
- Modify: `internal/board/board.go`, `internal/board/forums.go`, `internal/board/topics.go`, `internal/web/auth.go`, `internal/web/topic.go`, `internal/web/forum.go`, the index, forum and topic templates, `internal/web/static/gobb.css`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  - `0004_visits.sql`: `alter table users add column last_active_at timestamptz null`.
  - `board.Viewer` gains `LastVisit, MarkFloor time.Time`. `LastVisit` is `last_visit_at`, or `registered_at` when that is null. `MarkFloor` is `last_mark_at`, or `registered_at` when that is null. Both are zero for a guest.
  - `board.RecordVisit(ctx, q, userID int64, now time.Time) error`: the visit rule in "Decisions". Sign-in and every member request call it.
  - `board.TopicRow.New bool` and `board.Forum.New bool`. A forum is new when it, or a descendant that `v` may read, holds an unread topic that `v` may read.
  - `board.MarkTopic(ctx, q, userID, topicID int64, upTo time.Time) error`: it upserts `topic_marks` with the later of the stored and the given time.
  - `board.MarkForum(ctx, q, v Viewer, forumID int64, now time.Time) error`: it marks the forum and every descendant that `v` may read.
  - `board.MarkAll(ctx, q, userID int64, now time.Time) error`: it sets `last_mark_at`.
  - `board.CountView(ctx, q, topicID int64) error`.
  - `board.NewRow{TopicRow; Forum *Forum}`, `board.NewPage{Topics []NewRow; Page, Pages int}` and `board.NewSince(ctx, q, v, page int) (NewPage, error)`. It lists the readable, visible topics that are not markers, with `last_post_at` after `v.LastVisit`, newest first. `New` marks the unread ones.
  - `board.FirstUnread(ctx, q, v, topicID int64) (postID int64, err error)`: the first visible post after the member's mark for the topic, or the last post when none is newer.
  - `web.isNavigation(r *http.Request) bool`: true when `Sec-Fetch-Dest` is `document`, or when that header is absent and `Accept` contains `text/html`.
  - Routes: `GET /new` (a guest gets 302 to `/login?next=…`), `GET /t/{id}/new`, `POST /f/{id}/read`, `POST /read`.

Rules:
- A topic page view that `isNavigation` accepts calls `CountView`. For a member it also calls `MarkTopic` with the newest `created_at` among the posts on that page. A page that shows only old posts does not mark the newer posts read.
- The nav shows a member "New since your last visit (N)", where N is the count of `NewSince` rows.
- A "New" badge in `--new` marks unread forums on the index and unread topics in forum lists. "Mark this forum read" (on a forum page) and "Mark all read" (on the index) are POST forms.

- [ ] **Step 1: Write the failing tests**
  - `TestRecordVisit` (board), on bob, with imported `last_visit_at` 2026-09-30 19:15 UTC and a null `last_active_at`:
    - at T = 2026-10-24 12:00 UTC, `last_visit_at` stays and `last_active_at` becomes T;
    - at T+3 minutes, nothing changes;
    - at T+10 minutes, only `last_active_at` moves;
    - at T+2 hours, `last_visit_at` becomes T+10 minutes and `last_active_at` T+2 hours.
  - `TestUnreadRule` (board):
    - carol (null `last_mark_at`, registered 2020-01-15) sees topic 1 as `New`, and forum 2 as `New` in `Index`;
    - bob (`last_mark_at` 2026-09-30) sees nothing new;
    - after `MarkTopic(carol, 1, 2020-02-01 11:00 UTC)`, topic 1 is not new;
    - after `MarkForum(carol, 2)`, forum 2 is not new;
    - after `MarkAll(carol)`, nothing is new.
  - `TestNewSince` (board): with bob's `last_visit_at` set to 2019-06-01, bob's list holds topic 1 and not topic 2, which is in forum 3. With alice's set to the same date, alice's list holds both, and neither list holds the marker, topic 3.
  - `TestFirstUnread` (board): carol on topic 1 gets post 3. After `MarkAll`, she gets post 4, the last.
  - `TestTopicViewMarks` (web):
    - carol's `GET /t/1` with `Sec-Fetch-Dest: document` writes `topic_marks (3, 1)` with 2020-02-01 11:00 UTC, and `view_count` goes from 42 to 43;
    - the same request with `Sec-Fetch-Dest: image` writes no mark and adds no view;
    - `/t/1/new` answers 302 to `/t/1#p3` for carol.
  - `TestMarkReadForms` (web): carol's `POST /f/2/read` and `POST /read`, each with CSRF, answer 303 and remove the `New` badges. Without CSRF they answer 403.
  - `TestNewPage` (web): `/new` as a guest answers 302 to `/login?next=%2Fnew`. As bob, it lists the topics from `TestNewSince` with the forum name.
  - `TestGetHasNoSideEffects` (web). `getPaths` is a list in this file, and each later task adds its GET paths to it. Signed in as carol, request every path with `Sec-Fetch-Dest: image`, then compare a dump of every data table taken before and after. The dump leaves out `sessions` and the users columns `last_active_at` and `last_visit_at`. The two dumps are equal.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/board/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Compute `New` in the same queries that list the topics and forums: a left join to the member's marks, with no query per row. Add `/new` to `TestBasePathEverywhere`.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: new since your last visit and read marks" && git push`

**Checkpoint:** a member who signs in sees "New since your last visit" with the topics posted since their last visit, from the imported read marks. Red "New" badges mark unread forums and topics. Opening a topic, "Mark this forum read" and "Mark all read" each clear the badges.

---

### Task 5: Profiles, the member list, the team page and static pages

**Files:**
- Create: `internal/board/users.go`, `internal/board/users_test.go`, `internal/board/pages.go`
- Create: `internal/web/users.go`, `internal/web/templates/profile.html`, `internal/web/templates/members.html`, `internal/web/templates/team.html`, `internal/web/templates/page.html`, `internal/web/users_test.go`
- Modify: `internal/web/site.go`, `internal/web/templates/layout.html`, `internal/web/sideeffect_test.go`, `internal/web/basepath_test.go`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces:
  - `board.ProfilePage{Card UserCard; LastVisit *time.Time; Signature *markup.Node; Groups []string}` and `board.Profile(ctx, q, v, userID int64) (ProfilePage, error)`.
    - A gone user gives `ErrGone`, before any permission check.
    - An unknown ID gives `ErrNotFound`.
    - Without `account.view_profiles`, it gives `ErrForbidden`.
    - `Groups` lists the confirmed groups whose `kind` is not `hidden`.
  - `board.MembersPage{Members []UserCard; Sort string; Page, Pages int}` and `board.Members(ctx, q, v, sort string, page int) (MembersPage, error)`. It needs `account.view_profiles`. It lists active users only. `sort` is `name` (ascending by `username_clean`, the default), `joined` (ascending) or `posts` (descending); any other value is `name`.
  - `board.TeamGroup{Name string; Members []UserCard}` and `board.Team(ctx, q) ([]TeamGroup, error)`. It lists the owners, then the confirmed members of the built-in `admins` group, then those of `moderators`. Each person appears once, in the first group that holds them. Anyone may see it.
  - `board.StaticPage{Slug, Title string; Tree *markup.Node; UpdatedAt time.Time}`, `board.Page(ctx, q, slug string) (StaticPage, error)` and `board.PageLinks(ctx, q) ([]StaticPage, error)`, in slug order without trees, for the footer.
  - Routes: `GET /u/{id}`, `GET /members`, `GET /team`, `GET /pages/{slug}`, `GET /account`.

Rules:
- The profile shows the avatar, the name, the rank, the post count, the join date, the last visit date, the groups, the signature, and a "Find posts by this user" link to `/search?author=<name>`. It never shows the email address.
- `ErrGone` answers 410 with `This account no longer exists.` `ErrForbidden` answers 403, with a sign-in link for a guest.
- A static page renders its stored tree. Plan 5's editor parses again with the page's stored flags (Plan 2 carried).
- The nav gains Members (with `account.view_profiles`) and Team. The footer lists the static pages.

- [ ] **Step 1: Write the failing tests**
  - `TestProfile` (board): bob viewing user 1 gets alice with rank `Newcomer`, 120 posts, the groups `admins` and `members`, and a signature. A guest gets `ErrForbidden`. User 8 gives `ErrGone` for a guest and for bob. User 99 gives `ErrNotFound`.
  - `TestMembers` (board): bob gets alice, bob and carol, not dave. With `sort=posts`, the order is alice, bob, carol. A guest gets `ErrForbidden`.
  - `TestTeam` (board): alice is in the first group (an owner), bob in `moderators`, and nobody appears twice.
  - `TestProfilePages` (web):
    - `/u/1` as bob answers 200, with `<bdi>alice</bdi>`, `src="/files/ranks/newcomer.png"` and `Example signature`, and without `alice@example.com`;
    - as a guest it answers 403 with a `/login` link;
    - `/u/8` answers 410 and `/u/99` 404.
  - `TestMemberAndTeamPages` (web): `/members?sort=posts&page=2` as bob answers 404, because there is one page. `/team` as a guest answers 200, with alice and bob.
  - `TestStaticPages` (web): `/pages/about` has `This is an example board.`, `/pages/nope` answers 404, and the footer of `/` links `/pages/about` and `/pages/rules`.
  - `TestAccountRedirect` (web): a guest gets 302 to `/login?next=%2Faccount`, and bob gets 302 to `/u/2`.
  - Add `/u/1`, `/u/8`, `/members`, `/team`, `/pages/about` and `/account` to `getPaths` and to `TestBasePathEverywhere`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/board/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: profiles, members, team and static pages" && git push`

**Checkpoint:** any author name opens that member's profile. Members and Team list the people, the footer pages open, and a removed member's old profile link answers "410 Gone".

---

### Task 6: Search

**Files:**
- Create: `internal/board/search.go`, `internal/board/search_test.go`
- Create: `internal/web/search.go`, `internal/web/templates/search.html`, `internal/web/search_test.go`
- Modify: `internal/web/site.go`, `internal/web/templates/layout.html`, `internal/web/sideeffect_test.go`, `internal/web/basepath_test.go`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–5; the `posts.search` column and its GIN index.
- Produces:
  - `board.SearchQuery{Text string; ForumID int64; Author string; Sort string; Page int}`.
  - `board.Hit{Post PostRef; Forum *Forum; Snippet template.HTML}` and `board.SearchPage{Hits []Hit; Total, Page, Pages int}`.
  - `board.Search(ctx, q, v, sq SearchQuery) (SearchPage, error)`. Without `account.search`, it gives `ErrForbidden`.
  - Route: `GET /search?q=&forum=&author=&sort=&page=`.

Rules (spec 9.1, 7):
- Search covers only forums where `v` has both `forum.search` and `forum.read`, and only approved posts in approved topics that are not markers.
- `Text` becomes `websearch_to_tsquery('english', …)`. A post matches on its `search` column. The first post of a topic also matches on the topic's title.
- `Author` matches `username_clean` through `account.CleanUsername`. An unknown author gives no hits. `ForumID` 0 means all forums.
- `Sort` is `new` (`created_at` descending, the default) or `best` (`ts_rank` descending).
- `Text` over 256 bytes answers 400. An empty `Text` with no `Author` shows the form alone. A page past 40 answers 404, so a query never reads past 1,000 hits.
- The snippet comes from `ts_headline` over `subject || ' ' || body_text`, with `StartSel` U+E000 and `StopSel` U+E001. Remove those two runes from the text first. Escape the result for HTML, then turn the markers into `<mark>` and `</mark>`.
- The nav shows Search with `account.search`.

- [ ] **Step 1: Write the failing tests** (each grants `account.search` and `forum.search` as the case needs)
  - `TestSearchScope` (board):
    - with guests granted `account.search` and `forum.search` on forum 2, `example` finds post 1, and `moderator` finds nothing, because post 3 is unapproved;
    - with `forum.search` but not `forum.read` on forum 3, `color` finds nothing;
    - alice, with both on forum 3, finds posts 5 and 6 for `color`.
  - `TestSearchTitle` (board): with topic 1's title set to `Zebra crossing`, `zebra` finds post 1 alone, through the title. `welcome` lists post 1 once, though its subject and its topic's title both match.
  - `TestSearchAuthor` (board): as alice with rights on both forums, the author `BOB` with empty text gives posts 5 and 2, newest first.
  - `TestSearchSnippetEscapes` (board): with post 6's source set to `<script>alert(1)</script> needle`, the snippet for `needle` has `&lt;script&gt;` and `<mark>needle</mark>`, and no `<script`.
  - `TestSearchPage` (web): a guest without `account.search` gets 403. `q` of 257 bytes gets 400. `page=41` gets 404. A hit links to `/p/{id}`.
  - Add `/search?q=example` to `getPaths` and to `TestBasePathEverywhere`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/board/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** One query for the hits and one for the total.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/board/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: search with forum and author filters" && git push`

**Checkpoint:** Search finds posts by words in the title or the body, filters by forum and by author, and marks the matched words. It never shows a post from a forum the member cannot read.

---

### Task 7: Legacy routes and legacy file paths

**Files:**
- Create: `internal/legacy/legacy.go`, `internal/legacy/legacy_test.go`
- Create: `internal/web/legacy.go`, `internal/web/legacy_test.go`, `internal/web/leak_test.go`
- Modify: `internal/web/site.go`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–6; the `legacy_routes` and `legacy_files` tables.
- Produces:
  - `legacy.Route{Path string; Match map[string]string; IDParam, OffsetParam, Kind, TargetSlug string; TargetID int64; Position int}`.
  - `legacy.Table`, `legacy.Load(ctx, q db.Querier) (*Table, error)`, `(*Table).Match(path string, query url.Values) (Route, bool)` and `(*Table).File(path string) (dir, rest string, ok bool)`.
  - The site loads the table once, in `web.New`. Plan 2 carried: the server stops for an import.

Matching (`docs/bundle.md`, `legacy_routes.jsonl`): a route applies when the request path under the base path equals `Path`, each `Match` pair equals the first value of that query parameter, and the request has `IDParam` (when the route sets one). Of the routes that apply, the one with the lowest `Position` wins, then the lowest row ID. `File` matches the longest `prefix` of the path.

Answers. The site's own routes win. The legacy table runs only when no route of the site matched. Every target passes the same permission check as its page, before the redirect.

| Kind | Answer |
|---|---|
| `home`, `members`, `team`, `search`, `account` | 301 to `/`, `/members`, `/team`, `/search`, `/account` |
| `forum`, `topic` | 301 to `/f/{id}` or `/t/{id}`. With `OffsetParam`, add `?page=` the offset divided by the page size, plus 1, when that is above 1. |
| `post` | 301 to `/p/{id}`, which answers 302 to the viewer's page. A permanent redirect cannot hold a page that depends on the viewer. |
| `user` | 301 to `/u/{id}`; a gone user answers 410 at once |
| `attachment` | 301 to `/a/{id}` when `AttachmentFile` allows it, else its 403 or 404 |
| `avatar` | 301 to `/files/<avatar_path>`; no avatar gives 404 |
| `page` | 301 to `/pages/{slug}` |

- An `IDParam` value that is not an ID, an offset that is not a whole number of 0 or more, a target that does not exist, and a target the viewer cannot read each answer 404.
- A legacy file answers 301 to `/files/<dir>/<rest>` when that file would serve. Otherwise it answers 404.
- At load, the site logs a warning for each route whose `Path` equals one of its own route paths. That route can never apply.

- [ ] **Step 1: Write the failing tests**
  - `TestMatch` (legacy), on `Small()`'s three routes plus cases built in the test:
    - `old/thread.cgi` with `view=full&id=1` matches route 1;
    - `view=brief&id=1` and `view=full` (no `id`) do not match;
    - an added route on the same path with no `match`, `id_param` `p`, kind `post` and position 0 wins for `view=full&id=1&p=4`;
    - two routes that apply with equal positions give the lower row ID.
  - `TestFile` (legacy): `old/images/smilies/smile.gif` gives `smilies`, `smile.gif`. A longer prefix wins over a shorter one. `old/other/x.gif` gives false.
  - `TestLegacyRedirects` (web):
    - `/old/thread.cgi?view=full&id=1` answers 301 to `/t/1`, and with `&page=25` to `/t/1?page=2`;
    - `id=2` answers 404 for a guest (forum 3), and `id=abc` answers 404;
    - `/old/profile.cgi` answers 410;
    - `/old/about.cgi` answers 301 to `/pages/about`;
    - `/old/images/smilies/smile.gif` answers 301 to `/files/smilies/smile.gif`, and `/old/images/smilies/nope.gif` 404;
    - with `BasePath` `/forum/`, `/forum/old/thread.cgi?view=full&id=1` answers 301 to `/forum/t/1`.
  - `TestLegacyCollisionWarns` (web): a route with path `search` logs one warning that names it, through a test `slog` handler.
  - `TestHiddenForumNeverLeaks` (web). As a guest, on `Small()` plus one legacy route for each kind that points into forum 3 (topic 2, post 5) and at attachment 1 (moved into topic 2 for this test):
    - `/t/2`, `/p/5`, `/a/1` and each legacy route answer 404;
    - `/t/3` (whose target, topic 1, is readable) answers 301;
    - the bodies of `/`, `/f/2`, `/f/1` and `/search?q=color` (with search granted to guests) never hold `Favorite color` or `Green, for me`.

    Then make topic 2 `global`, and `/f/2` still holds neither.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/legacy/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/legacy/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: legacy routes and file paths" && git push`

**Checkpoint:** an old link from the former board, pasted under the base path, lands on the matching gobb page with a 301. An old smiley or rank image URL shows the image, and an old link into a hidden forum answers 404.

---

### Task 8: Login limits

**Files:**
- Create: `internal/ratelimit/ratelimit.go`, `internal/ratelimit/ratelimit_test.go`
- Create: `internal/login/gate.go`, `internal/login/gate_test.go`
- Modify: `internal/password/recognize.go`, `internal/password/recognize_test.go`, `internal/login/login.go`, `internal/login/login_test.go`, `internal/session/session.go`, `internal/session/session_test.go`, `internal/web/auth.go`, `internal/web/auth_test.go`, `internal/web/site.go`, `cmd/gobb/main.go`, `docs/changelog.md`

**Interfaces:**
- Consumes: Task 3.
- Produces:
  - `ratelimit.New(limit int, window time.Duration, maxKeys int) *Limiter`. `(*Limiter).Over(key string, now time.Time) (bool, time.Duration)` reports whether `key` has `limit` events inside `window`, and how long until one expires. `(*Limiter).Add(key, now)` records one event. `(*Limiter).Reset(key)` clears a key. When the map holds `maxKeys` keys, `Add` first drops the expired keys, then the key with the oldest last event.
  - `password.MemoryKiB(scheme, hash string) uint64`: the memory that checking the hash needs. For an argon2id hash it is its `m`; for every other hash it is 1,024. It runs no hash.
  - `login.Gate`, `login.NewGate(budgetKiB uint64) *Gate` and `(*Gate).Acquire(ctx context.Context, kib uint64, wait time.Duration) (release func(), err error)`. A cost above the budget counts as the whole budget, so it runs alone. When the wait ends without room, it returns `login.ErrBusy`.
  - `login.Options{IPLimit, NameLimit *ratelimit.Limiter; Gate *Gate; Verify func(raw, stored, scheme string) (bool, bool)}` and `login.NewWith(q db.Querier, opt Options) *Service`. `login.New` uses the defaults below and `password.Verify`. A test may replace `Verify`.
  - `*login.LimitedError{RetryAfter time.Duration}` and `login.ErrBusy`.
  - `session.Cleanup(ctx, q, now time.Time) (sessions, ips int64, err error)`. It deletes expired sessions and the `user_ips` rows last seen more than 90 days ago (spec 10).

Defaults (spec 19 leaves them to the builder):
- Per IP: 10 failed sign-ins in 15 minutes. Per name: 20 failures in 1 hour, keyed on `CleanUsername(name)` whether or not an account has it. `maxKeys` is 10,000 for each.
- `Check` tests both limits before any lookup or hash, and gives `LimitedError` with the longer wait. A failure adds to both. A success resets the name.
- The gate budget is 128 MiB (131,072 KiB) and the wait 5 s. Each `Verify`, and each dummy check, holds `MemoryKiB` of the gate.
- An unknown name runs one dummy `Verify` against a fixed gobb hash, so a missing account takes as long as a wrong password.
- `POST /login` bodies are capped at 16 KiB, and every other POST at 4 KiB, with `http.MaxBytesReader`. A larger body answers 413.
- `LimitedError` answers 429 with `Retry-After` and `Too many sign-in attempts. Try again in N minutes.` `ErrBusy` answers 503 with `Retry-After: 5`.
- `serve` runs `session.Cleanup` once an hour, and logs the two counts.

- [ ] **Step 1: Write the failing tests**
  - `TestLimiter` (ratelimit): 3 events in a 1-minute window with limit 3 give `Over` true, with a wait under 1 minute. After the window, `Over` is false. `Reset` clears the key. With `maxKeys` 2, a third key drops the oldest.
  - `TestMemoryKiB` (password): `password.Hash("x")` gives 19,456. A legacy `$argon2id$v=19$m=65536,t=1,p=1$…` gives 65,536. bcrypt gives 1,024.
  - `TestGate` (login): with a budget of 100, two holders of 60 cannot run together: the second waits until the first releases. A holder of 500 runs alone. A full gate with a 50 ms wait gives `ErrBusy`.
  - `TestLoginMemoryBudget` (login): 20 goroutines call `Check` at once, against a user whose hash costs 65,536 KiB. A fake `Verify` sleeps 20 ms and records how many run at the same time. No more than 2 run together (128 MiB / 64 MiB), and every call returns.
  - `TestCheckLimits` (login):
    - 10 wrong passwords from `10.0.0.9` make the 11th try, with the right password, give `LimitedError`, and no `Verify` runs for it;
    - the same name from another IP still works until its own 20 failures;
    - the unknown name `nobody` runs `Verify` exactly once.
  - `TestCleanup` (session): an expired session and a `user_ips` row 91 days old are deleted. A live session and a row 89 days old stay.
  - `TestLoginHTTPLimits` (web): a 1 MiB `POST /login` answers 413. After 10 failures from one address (behind a trusted proxy, so the address comes from `X-Forwarded-For`), the next try answers 429 with `Retry-After`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/ratelimit/ ./internal/password/ ./internal/login/ ./internal/session/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Write the gate with a mutex and a `sync.Cond`, or a channel that wakes waiters; do not add `golang.org/x/sync`.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/ratelimit/ ./internal/password/ ./internal/login/ ./internal/session/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "login: rate limits, a memory gate and body caps" && git push`

**Checkpoint:** after ten wrong passwords from one browser, the sign-in page says how long to wait, and the right password works again after the wait.

---

### Task 9: Security headers and the CSP

**Files:**
- Create: `internal/web/headers.go`, `internal/web/headers_test.go`, `internal/web/templates_test.go`
- Modify: `markup/render.go`, `markup/render_test.go`, `internal/web/site.go`, `internal/web/topic.go`, `internal/web/templates/layout.html`, `internal/web/templates/topic.html`, `docs/changelog.md`

**Interfaces:**
- Consumes: Tasks 1–8; `markup.Rendered.FrameSrc` and `ScriptSrc`.
- Produces:
  - `markup.Rendered.ConnectSrc []string` and `markup.Rendered.Scripts []string`, sorted and without duplicates, like `FrameSrc`. `Scripts` holds the full URLs of the provider scripts that the tree's embeds need. They come from the same embed table as the rest of each entry (spec 12.2: "add one entry to the embed table: its link patterns, its output, and the origins it needs").
  - `web.policy{Frame, Script, Connect []string; InlineStyle bool}` and `(policy) String() string`.

The policy:
- Every response has this CSP, plus additions:

  ```
  default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' https:; connect-src 'self'; font-src 'self'; frame-src 'none'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'; object-src 'none'
  ```

  `img-src` allows `https:`, because post images stay hotlinked and the renderer upgrades `http` to `https` (spec 12.2).
- A page that renders posts merges the `FrameSrc`, `ScriptSrc` and `ConnectSrc` of every post and signature on it into `frame-src`, `script-src` and `connect-src`. `frame-src 'none'` gives way to the list.
- It adds one `<script async src="…">` to the page for each URL in `Scripts`.
- A page with any script embed also adds `'unsafe-inline'` to `style-src`, because the provider scripts set style attributes. gobb's renderer never writes a style attribute, so post text gains nothing from this.
- **X:** the `tweet` entry adds X's syndication hosts to `ConnectSrc` (roadmap, Plan 1 carried). Take the host list and the script URLs for X, Instagram, Reddit and Threads from each provider's published embed documentation. Cite each page in a comment by its entry, and say in the report what the docs list. `img-src https:` already covers the image hosts.
- Every response, errors and files included, has `X-Content-Type-Options: nosniff`, `Referrer-Policy: strict-origin-when-cross-origin`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin` and `Permissions-Policy: camera=(), microphone=(), geolocation=()`.
- `/login`, `/new`, `/search` and every error page have `<meta name="robots" content="noindex">`.

- [ ] **Step 1: Write the failing tests**
  - `TestRenderScriptsAndConnect` (markup): a `tweet` gives `Scripts` with X's widget script URL and a non-empty `ConnectSrc`. A `youtube` gives neither. Two tweets give one entry each.
  - `TestCSPBase` (web): `/` has the exact base policy above, and so do `/nope` (404) and `/files/avatars/1.png`.
  - `TestCSPEmbeds` (web):
    - with post 1's source set to a bare YouTube link, `/t/1` gets `frame-src https://www.youtube-nocookie.com`, keeps `script-src 'self'`, and has no `'unsafe-inline'`;
    - with post 2's source set to a bare X post link, `/t/1` gets `https://platform.twitter.com` in `frame-src` and `script-src`, X's hosts in `connect-src`, `'unsafe-inline'` in `style-src`, and one `<script async src="` with X's script URL;
    - `/` keeps the base policy.
  - `TestSecurityHeaders` (web): the five headers on `/`, `/t/1`, `/nope`, `/a/1` (with download rights) and `/healthz`.
  - `TestTemplatesHaveNoInlineScriptOrStyle` (web): no embedded template has `<script>` without `src`, a `style=` attribute, a `<style` element, or an `on…=` attribute.
  - `TestNoindex` (web): the `robots` meta is on `/login`, `/search` and a 404, and not on `/t/1`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./markup/ ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Set the headers in one middleware around the whole handler. A handler that renders posts passes its `policy` to the page writer, and the writer sets the CSP header before it writes the body.
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./markup/ ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "web: security headers and per-page CSP" && git push`

**Checkpoint:** a topic with a YouTube video and an X post shows both. The browser console reports no CSP violation on the index, a topic, a profile and the sign-in page.

---

### Task 10: The image serves pages under a memory cap

**Files:**
- Create: `.github/compose.memcap.yaml`, `scripts/smoke-serve.sh`
- Modify: `.github/workflows/ci.yml` (the `image` job), `README.md`, `docs/changelog.md`

**Interfaces:**
- Consumes: every earlier task; `docs/example-bundle/`; the user `alice` with the made-up password `example-password`.
- Produces:
  - `.github/compose.memcap.yaml`: an override that sets `mem_limit: 256m` on `gobb` and `GOBB_COOKIE_SECURE: "false"`, used together with `.github/compose.ci.yaml`.
  - `scripts/smoke-serve.sh <base-url>`: POSIX `sh` and `curl`. It exits non-zero on the first failure, and prints the URL and the status it got.

The smoke run, after the existing `up --wait` in the `image` job, with the memcap override:
1. `docker compose … run --rm -v "$PWD/docs/example-bundle:/import:ro" gobb import /import` loads the example bundle.
2. `docker compose … port gobb 8080` gives the host port.
3. `scripts/smoke-serve.sh` checks:
   - `/`, `/f/2`, `/t/1` and `/login` answer 200, and `/t/3` answers 301;
   - `/u/8` answers 410;
   - each 200 has a `Content-Security-Policy` header;
   - a sign-in as alice (GET `/login` with a cookie jar, read `csrf` from the form, POST) answers 303, and the next `/` holds `Signed in as`.
4. `docker inspect` shows that the container did not restart and was not OOM-killed.

- [ ] **Step 1: Write the failing check.** Add the smoke steps to the `image` job, and write `scripts/smoke-serve.sh` with every check above. Start the stack locally with `docker compose -f compose.yaml -f .github/compose.ci.yaml -f .github/compose.memcap.yaml up -d --build --wait`, and run the script before the import. Expected: FAIL at the first URL, because the database holds no board. Run `colima start` first if Docker is not running.
- [ ] **Step 2: Run it after the import.** Import the example bundle, then run the script again. Expected: every check passes, and `docker inspect -f '{{.RestartCount}} {{.State.OOMKilled}}'` prints `0 false`. Then `docker compose … down -v`.
- [ ] **Step 3: Update the README.** In "Run the server", add how to sign in, `GOBB_COOKIE_SECURE`, and the old-URL behavior. Run every command in the section against the Compose stack, and correct them until they work.
- [ ] **Step 4: Verify on GitHub.** `git add -A && git commit -m "ci: serve pages under a memory cap in the image job" && git push`, then `gh run watch --exit-status $(gh run list --workflow ci.yml --limit 1 --json databaseId -q '.[0].databaseId')`. Expected: the `check` and `image` jobs pass.

**Checkpoint:** the owner runs the published image with Docker Compose against the private copy, and browses, signs in and searches. The `gobb` container stays under 256 MiB.

---

## Order

The tasks run in order, one at a time. Each slice builds on the one before it, and the owner looks at each checkpoint before the next task starts. The markup half of Task 9 (`ConnectSrc`, `Scripts`) touches only `markup/`, so it may start while Task 8 runs.

## End of plan

When Task 10 is green in CI, the Opus reviewer reviews the whole plan's work against the spec sections named in the header, and against each Plan 3 item in the roadmap's two "Carried" sections. Then the next plan (Write side) is written against the code as it stands.
