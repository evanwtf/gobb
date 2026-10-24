# gobb Plan 4: Lean release 1 and cutover — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Members of the first board can post, reply, quote, edit and delete their own posts, and change their password, email and signature. Moderators can delete, restore, edit, approve and lock in place. The admin can reset a password and ban a member from the command line. gobb ships as a tagged release, and the first board cuts over to it.

**Architecture:** `internal/board` gains the write functions. Each one takes a `Viewer`, checks the rights itself, and changes the rows and their counters in one transaction. `internal/web` adds the forms and the POST handlers. Each handler calls one board function, then answers 303 to a GET page. Every text that a member writes takes one path: `markup.Parse`, then `markup.Validate`, then storage of the source, the tree and the plain text. No HTML is stored. `cmd/gobb` gains `user` commands. A release workflow builds the binaries and the image on a tag.

**Tech Stack:** Go 1.27 standard library; pgx v5; `golang.org/x/net/html`, in tests only (see Global Constraints). GitHub Actions on the self-hosted runners, Docker Buildx, GitHub Container Registry.

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md`, sections 6, 7 ("Posting" and "Accounts"), 9, 10, 11 ("In place"), 12, 15, 17 ("Release 1 — cutover, lean") and 18. Roadmap: `docs/superpowers/plans/2026-10-23-gobb-roadmap.md`, Plan 4's row and "Carried from Plan 3".

**Workers:** Sonnet, high effort. Reviewer: Opus, high effort, after every task. After Task 6, a separate Opus security review covers Tasks 1–6. An Opus review of the whole plan comes at the end.

## Global Constraints

- **Clean room.** Work only from this repo and public standards (RFCs; the Go, PostgreSQL, GitHub Actions and Docker docs; the WHATWG HTML standard; public write-ups of script-injection techniques). Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. Name no other forum software in code, docs, tests or commits. (`AGENTS.md`; spec 1.)
- **Real data stays with the controller.** Workers use `bundletest` fixtures and made-up data only. The controller runs each checkpoint against its private copy of the first board.
- This repo is public. Never commit hostnames, IP addresses other than `127.0.0.1`, `10.x` and `2001:db8::/32`, home-directory paths, secret values or forum data.
- Module `github.com/evanwtf/gobb`, the latest Go 1.27.x. **One new dependency, in tests only:** `golang.org/x/net/html`, for the HTML checker of Task 1. Record the reason in the changelog. Nothing else.
- Commit subjects: `area: imperative summary`. No agent session URLs or IDs, and no agent trailers. Work lands on `main`. Push after every task. Each task adds its bullet to `docs/changelog.md` under `## 0.1.0 (unreleased)`.
- Tests that need PostgreSQL use `dbtest.New` (through `boardtest.Load`) and skip when `GOBB_TEST_DATABASE_URL` is unset. CI sets it and fails on any skip.
- **Migrations.** Never edit a migration that is already on `main`. A task that needs schema adds the next numbered file. The release-1 schema already holds every column this plan writes, so no task is expected to need one.
- **One write path for member text. (owner: security)** A post, an edit, a signature and a preview each go through `board.ParseBody` (Task 2). It runs `markup.Parse` with the flags and rights of the writer, then `markup.Validate`. It returns the tree, its JSON and its plain text. Nothing stores or echoes HTML from a request. The only HTML a page shows comes from `markup.Render` and `html/template`.
- **Rights are checked in `internal/board`, not only in `internal/web`.** A board write function returns `ErrForbidden` or `ErrNotFound` on its own. Hiding a button is never the check. A target that the viewer cannot see gets `ErrNotFound`, the same as a missing one (spec 14).
- **GET and HEAD change nothing,** as in Plan 3: a form page writes nothing. Every change is a POST through `site.protect` (Cross-Origin Protection and the CSRF token). A successful POST answers 303 to a GET page.
- **The form keeps the member's text.** A POST that fails a check answers 400 or 403 with the form again, holding the text the member sent and a message that names the problem.
- **Every moderator and admin action writes one `mod_log` row:** `actor_id` is the moderator, or null for a command-line action. `action` is one of `post.delete`, `post.restore`, `post.approve`, `post.edit`, `topic.lock`, `topic.unlock`, `user.ban`, `user.unban` and `user.reset_password`. `details` holds `{"via":"cli"}` for a command-line action, and the edit reason for `post.edit`. A member who edits or deletes their own post writes no row.
- **Counters stay exact.** After any write, the counters of every topic and forum, and the last-post columns, equal what the importer's full recompute would give. A test proves it (Task 2).
- **No inline script, no `style` attributes and no `<style>` elements** in templates (Plan 3, Task 9). New pages keep the CSP of Plan 3.
- **Limits.** A title is 1–120 characters after trimming. A post body is 1–60,000 bytes. A signature is 0–2,000 bytes. An edit reason is 0–255 characters. A new password is at least 10 characters and at most 1,024 bytes, and must differ from the username. The POST body cap for the posting forms is 256 KiB, since a 60,000-byte body percent-encodes to at most 180,000 bytes.
- **Checkpoints.** Each task ends with a `Checkpoint:` line. The controller runs it on its private copy before the next task starts.

## Review Focus

1. **Quoting or replying to what the viewer cannot see.** `/t/{id}/reply?quote={post}` for a post in a hidden forum, or for an unapproved or deleted post, must not put its text into the form. Test: `TestQuoteHiddenPostIsNotFound` (Task 3).
2. **A second click, or a reload after the POST.** It must not create a second, identical post. Test: `TestDuplicatePostIsOne` (Task 2).
3. **A form that fails a check loses the member's text.** A member who wrote 2,000 words and hit a limit must get the text back. Test: `TestFormKeepsTextOnError` (Task 3).
4. **Counters and the last-post column drift** after a delete, a restore, an approval or a lock. The index would then show a deleted post as the last post. Test: `TestCountersMatchRecompute` (Task 2).
5. **Changing an ID in the URL or the form.** A member who POSTs to `/p/{someone else's post}/edit` or `/delete` must get 404 or 403, never a change. Test: `TestWritesCheckOwnership` (Task 2).

---

## Decisions this plan fixes

**URLs.** All are under the base path. Plan 3's URLs stay.

| Path | What |
|---|---|
| `GET /f/{id}/post`, `POST /f/{id}/post` | the new-topic form, and the new topic |
| `GET /t/{id}/reply?quote={post}`, `POST /t/{id}/reply` | the reply form (with a quote when asked), and the reply |
| `GET /p/{id}/edit`, `POST /p/{id}/edit` | the edit form, and the edit |
| `POST /p/{id}/delete`, `/restore`, `/approve` | delete, restore and approve a post |
| `POST /t/{id}/lock`, `/unlock` | lock and unlock a topic |
| `GET /account` | the account page (replaces Plan 3's redirect for a member) |
| `POST /account/password`, `/account/email`, `/account/signature` | the three account changes |

**Preview.** Each posting form has two submit buttons, "Post" and "Preview". With `preview` in the POST, the answer is 200 with the form and the rendered post above it, and nothing is saved. Preview runs `board.ParseBody` exactly as a save would. No JavaScript.

**Deferred from spec 7 (owner, 2026-10-24):** the markup toolbar and the smiley picker, attachment uploads, polls, private messages, watches, email, password reset by email, avatar upload, time zone, and topic kinds other than normal. The forms show a short line on Markdown under the text box.

**Who may do what.** "Can read" means the viewer sees the topic and the post under Plan 3's visibility rules.

| Action | Needs |
|---|---|
| New topic | `topic.start` in the forum; the forum is a `forum` kind; the forum is not locked, or the viewer has `mod.lock` there |
| Reply | `topic.reply`; can read the topic; the topic is approved; the topic and its forum are not locked, or the viewer has `mod.lock` |
| Quote | the reply rights, and can read the quoted post |
| Edit own post | `post.edit_own`; the author is the viewer; the topic is not locked |
| Edit any post | `mod.edit` in the forum |
| Delete own post | `post.delete_own`; the author is the viewer; the post is not the first post of a topic that has other approved posts |
| Delete any post | `mod.delete` in the forum |
| Restore | `mod.delete` in the forum |
| Approve | `mod.approve` in the forum |
| Lock or unlock | `mod.lock` in the forum, or `topic.lock_own` for the topic's author |

**Deleting a first post deletes its topic.** The topic's `visibility` becomes `deleted`, and its posts keep their own visibility. Restoring that post restores the topic.

**New posts.**
- A new post is approved when the writer has `post.no_approval` in the forum. Otherwise it is held, with visibility `unapproved`. A held new topic is held as a whole.
- The flags of a new post are Markdown = `post.markdown`, smilies = `post.smilies`, and URL linking on.
- An image needs `post.images`. Without it, a body that holds an `img` node is refused, with the message "You may not post images in this forum."
- An edit keeps the post's stored flags, so an imported post renders as it did.
- The author's topic mark moves to the new post's time, so the author's own post is not "new" to them.
- `users.post_count` grows by one for each approved post in a forum where the writer has `post.counts`. It shrinks when such a post is deleted, and grows again on restore.

**Duplicates.** A post whose author, topic and body source equal those of a post that the same author made in the last 60 seconds is not saved again. The write returns the earlier post, and the handler redirects to it. For a guest, "the same author" means the same address.

**Guest posts. (owner, 2026-10-24)** On the first board, one forum lets guests post: it is where a member who is locked out asks for help. Password reset by email waits until after cutover, so this forum is that member's only way in. gobb follows the grants, as for a member:
- A guest may start a topic or reply where the guests group holds `topic.start` or `topic.reply`. The post is held unless the guests group holds `post.no_approval` there.
- A guest gives a name of 1–40 characters. A name whose `account.CleanUsername` equals a member's `username_clean` is refused, with "That name belongs to a member. Sign in, or choose another name." So nobody can post as the admin.
- The post stores `author_id` null, the name in `author_name`, and the address in `author_ip`.
- A guest can never edit, delete, quote-edit or moderate. A guest has no topic marks and no post count.
- The owner may revisit guest posting after cutover (GitHub issue).

**Counters.** Task 2 moves the importer's two recompute statements into `internal/counters`. It adds one-row forms of each: `Topic(ctx, q, topicID)` and `Forum(ctx, q, forumID)`. The importer keeps calling the whole-board form. A write calls the one-row forms for each topic and forum it touches, in its own transaction.

**Release version.** The cutover release is `v0.1.0`, the version the changelog and `gobb version` already hold.

---

### Task 0: Carried from Plan 3's last review

Plan 3's final re-review approved the read side and left four Minors. Its report is `plan3-final-rereview.md` in this plan's workspace.

**Files:** `internal/login/` (the hold and the floor clock, and their tests), `internal/web/` (error answers, and their tests), `README.md`, `docs/changelog.md`.

- **R1. A shared-email sign-in holds K checks' memory at once.** `runJobs` collects only at release. Run `runtime.GC()` before each check after the first, so a finished check's memory is free before the next one starts, as the README says.
  - Test: a hook counts collections inside one hold of three checks, and expects two.
- **R2. The lookup sits outside the floor.** Since Plan 3's T8e, a missing name answers about 0.1 ms after an existing one. Move the floor's start forward by the gate wait only, not by the lookup time.
  - Test: with a fake lookup that takes 5 ms more for a missing name, the two answer times agree within 1 ms.
- **R3. Two tests cannot see what they name.** Make `TestHoldEndsWithACollection` fail when the release comes before the collection. Make `TestLoadFloorHoldsTheGate` fail without the collection after `LoadFloor`. Show both failures with a mutation.
- **R4. Plain text from the standard library.**
  - A bad `Range` header gets a plain-text 416 from `http.ServeContent`.
  - Under a base path, a percent-encoded letter of the base path gets a plain-text 404 from `http.StripPrefix`.
  - Answer both with the themed page (spec 13). Extend `TestNoPlainTextErrors` with each case.

- [ ] **Step 1: Write the failing tests and show each one fails.**
- [ ] **Step 2: Implement.**
- [ ] **Step 3: Run** `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips.
- [ ] **Step 4: Commit and push.** `git commit -m "login, web: fixes from the read side's last review"`

**Checkpoint:** a shared-email sign-in on the controller's capped copy stays near 137 MiB.

---

### Task 1: Harden the markup for writes

**Files:**
- Modify: `markup/parse.go`, `markup/render.go` (only as the fixes need), `markup/parse_test.go`, `markup/render_test.go`
- Create: `markup/allow.go`, `internal/htmlcheck/htmlcheck.go`, `internal/htmlcheck/htmlcheck_test.go`, `markup/testdata/xss/payloads.txt`
- Modify: `.github/workflows/ci.yml` (a `fuzz` job), `go.mod`, `go.sum`, `docs/changelog.md`

**Interfaces:**
- Consumes: `markup.Parse`, `markup.Validate` and `markup.Render`, unchanged in signature.
- Produces:
  - `markup.Allowed() map[string][]string`: each element that the renderer may write, with the attributes it may carry. The renderer and the check both read it, so they cannot disagree.
  - `markup.EmbedOrigins() []string`: the frame origins of the embed table.
  - `htmlcheck.Check(t testing.TB, fragment string)`. Only test files import `internal/htmlcheck`, so `golang.org/x/net/html` stays out of the `gobb` binary. Tasks 3 and 5 call it on each `.post-body` and `.signature` element of a page.

Rules:
- **The quadratic parse.** Fix it (roadmap, "Carried from Plan 3"). A repeated `[a](` is the known case.
- **Angle-bracket text.** Search finds text that appears only inside angle brackets (`<like this>`). `Rendered.Text` keeps the brackets. (Ruling in review: migration `0008` maps `<` and `>` to spaces inside the search expressions only, because PostgreSQL's text-search parser drops `<word ...>` as a tag.)
- **`htmlcheck.Check`** tokenizes HTML with `golang.org/x/net/html` and fails on any of these:
  - an element outside the renderer's allowlist;
  - an attribute outside that element's allowlist;
  - any attribute whose name starts with `on`, and any `style` attribute;
  - an `href` or `src` that fails the renderer's URL rules (`http`, `https`, `mailto`, or a site path);
  - an `iframe` whose `src` origin is not in the embed table;
  - any `script`, `style`, `object`, `embed`, `base`, `form`, `meta` or `link` element.
- **The payload corpus** is `markup/testdata/xss/payloads.txt`: one payload per line, at least 150 lines, written from public knowledge of script-injection techniques. It covers:
  - script tags in every case;
  - event handlers;
  - `javascript:`, `vbscript:` and `data:` in links and images, plain, entity-encoded, percent-encoded and with embedded white space or control characters;
  - SVG and MathML;
  - attribute breaks with `"`, `'` and backticks;
  - link titles; reference links; autolinks; HTML comments and CDATA;
  - nested quotes and spoilers; code spans and fences that hold HTML;
  - embed links with crafted IDs;
  - Unicode tricks: bidirectional controls, full-width characters and NUL.

- [ ] **Step 1: Write the failing tests**
  - `TestParseLinkRunLinear`: 10,000 repeats of `[a](` parse in under 50 ms. The time for 20,000 is less than three times the time for 10,000.
  - `TestParseNestingScales`: raise n until the small run takes at least 10 ms, or drop the additive term, so it fails for quadratic growth (roadmap).
  - `TestTextKeepsAngleBracketText`: `see <like this> here` gives a `Text` that contains `like this`.
  - `TestPayloadsRenderSafe`: each line of the corpus, parsed with every combination of the three flags, passes `markup.Validate` or is refused by it. Each result that passes renders, and its HTML passes `htmlcheck.Check`.
  - `TestCheckCatches` (in `internal/htmlcheck`): the check fails on each forbidden case in the rule list above. Use one hand-written fragment per case, so a check that passes everything cannot pass this test.
  - `FuzzParse` and `FuzzRender` call `htmlcheck.Check` in place of the old `<script` check.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./markup/`. Expected: FAIL, on the link run and on the angle brackets at least.
- [ ] **Step 3: Implement the fixes.**
- [ ] **Step 4: Add the CI `fuzz` job.**
  - It runs on `[self-hosted, Linux, X64]` after `check`.
  - It runs each fuzz target in the repo (`FuzzParse`, `FuzzRender`, `FuzzVerifyLegacy`) for 60 s, with `go test -run '^$' -fuzz '^Name$' -fuzztime 60s`.
  - A failure uploads the failing input as an artifact. The worker adds each failing input to `testdata/fuzz` and fixes the cause before landing.
- [ ] **Step 5: Run them to verify they pass.** Run: `go test -race ./markup/`, then each fuzz target for 60 s locally. Expected: PASS, no new failing inputs.
- [ ] **Step 6: Commit and push.** `git commit -m "markup: linear link parse, angle-bracket text, and a strict HTML check"`. Watch CI until the `fuzz` job is green.

**Checkpoint:** the controller parses every post on its private copy again. The count of trees that differ from the stored ones is 0, apart from the expected text change for angle brackets. The time for the whole set is reported.

---

### Task 2: The board write functions

**Files:**
- Create: `internal/counters/counters.go`, `internal/counters/counters_test.go`
- Create: `internal/board/write.go`, `internal/board/write_test.go`, `internal/board/modwrite.go`, `internal/board/modwrite_test.go`
- Modify: `internal/importer/counters.go` (to call `internal/counters`), `docs/changelog.md`

**Interfaces:**
- Consumes: `board.Viewer`, `acl.Set.Has`, Plan 3's visibility rules, and `markup` from Task 1.
- Produces:
  - `counters.All(ctx, q) error`, `counters.Topic(ctx, q, topicID int64) error` and `counters.Forum(ctx, q, forumID int64) error`.
  - `board.Body{Tree *markup.Node; JSON []byte; Text string; Flags markup.Options}` and `board.ParseBody(ctx, q, v Viewer, forumID int64, src string, flags *markup.Options) (Body, error)`. It parses with the board's smilies.
    - A nil `flags` means a new post, so the flags come from the viewer's rights in the forum.
    - A non-nil `flags` is an edit's stored flags.
    - `forumID` 0 means a signature: `flags` must be the member's stored signature flags, no forum right applies, and the limit is 2,000 bytes.
    - The error is `*board.InputError{Field, Message string}` for a limit, a refused image or a tree that fails `Validate`.
  - `board.Draft{Title, Source, GuestName string; IP netip.Addr}`. `GuestName` applies to a guest only. `IP` is the writer's address, stored in `author_ip` for every new post.
  - `board.NewTopic(ctx, pool, v, forumID int64, d Draft, now time.Time) (postID int64, err error)`.
  - `board.Reply(ctx, pool, v, topicID int64, d Draft, now time.Time) (postID int64, err error)`.
  - `board.EditPost(ctx, pool, v, postID int64, d Draft, reason string, now time.Time) error`. The title applies when the post is the first post of its topic.
  - `board.DeletePost`, `board.RestorePost` and `board.ApprovePost`, each `(ctx, pool, v, postID int64, now time.Time) error`.
  - `board.SetTopicLock(ctx, pool, v, topicID int64, locked bool, now time.Time) error`.
  - `board.QuoteSource(ctx, q, v, postID int64) (string, error)`. It returns the Markdown of a quote of a post the viewer can read, in the existing quote form `> **Name** [wrote](/p/{id}):` followed by the quoted lines.
- Each write runs in one transaction. It checks the rights by the table under "Who may do what", writes the rows, calls `counters.Topic` and `counters.Forum` for each topic and forum it touched, updates `users.post_count` and writes `mod_log` where the Global Constraints say so.

- [ ] **Step 1: Write the failing tests** (with `bundletest` fixtures)
  - `TestCountersOneRowEqualsAll`: after random changes to visibility on a fixture board, calling `Topic` and `Forum` for the touched rows leaves every counter equal to what `All` then computes.
  - `TestNewTopicAndReply`:
    - alice starts a topic in forum 2 and replies to it. The forum's `topic_count` and `post_count`, and the topic's `reply_count` and last post, change by the expected amounts.
    - alice's `post_count` grows by 2, and her topic mark equals the reply's time.
    - Without `post.no_approval`, the reply is `unapproved` and no counter changes.
  - `TestCountersMatchRecompute`: after each of NewTopic, Reply, EditPost, DeletePost (a reply, then a first post), RestorePost, ApprovePost and SetTopicLock, running `counters.All` changes no row.
  - `TestWritesCheckOwnership`:
    - carol, who has no moderator rights, gets `ErrForbidden` for EditPost and DeletePost on alice's post in a forum she can read.
    - On a post in a forum she cannot read, she gets `ErrNotFound`.
    - A guest gets `ErrForbidden` for EditPost, DeletePost and every moderator write, even where the guests group may post.
  - `TestGuestPost`:
    - with `topic.reply` granted to the guests group on forum 2, a guest reply with the name "Visitor" is saved, with `author_id` null, `author_name` "Visitor" and the guest's address;
    - it is held unless the guests group also holds `post.no_approval`;
    - the name "ALICE" is refused with the message under "Guest posts";
    - without the grant, a guest reply gets `ErrForbidden`.
    - Each test grants and removes the rights its case needs, so a fixture's moderator rights cannot make a refusal pass.
  - `TestWriteRights`: one case for each row of "Who may do what", allowed and refused, including:
    - a reply to a locked topic refused for a member and allowed with `mod.lock`;
    - a reply to an unapproved topic refused;
    - the delete of a first post that has replies refused for its author and allowed with `mod.delete`.
  - `TestDeleteFirstPostDeletesTopic`: the topic becomes `deleted`, the forum's counters drop, and a restore brings both back.
  - `TestDuplicatePostIsOne`: two Replies with the same source within 60 s return the same post ID, and the count of posts grows by one. After 61 s, a second post is saved.
  - `TestParseBodyRules`:
    - limits: 0 bytes and 60,001 bytes are refused, with `Field` `body`;
    - a title of 121 characters is refused;
    - an image without `post.images` is refused with "You may not post images in this forum.";
    - without `post.markdown`, `**x**` stays literal text;
    - for an edit, the stored flags win;
    - a source that holds `<script>` stores a tree whose HTML passes `htmlcheck.Check`.
  - `TestQuoteSourceHidden`: `QuoteSource` gives `ErrNotFound`:
    - for a post in a forum the viewer cannot read;
    - for an unapproved post of another member;
    - for a deleted post.
  - `TestModLog`: each moderator action writes exactly one row, with the action names of the Global Constraints. An author's own edit and delete write none.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/counters/ ./internal/board/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Move the two statements and add a `where` on the one-row forms. Keep one copy of each statement.
- [ ] **Step 4: Run them to verify they pass,** plus the importer tests. Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "board: write functions with rights, counters and the moderation log"`

**Checkpoint:** on its private copy, the controller replies to a topic as a made-up member that it adds, edits the reply, and deletes it. `counters.All` then changes no row.

---

### Task 3: Posting pages

**Files:**
- Create: `internal/web/post.go`, `internal/web/post_test.go`, `internal/web/templates/postform.html`
- Modify: `internal/web/site.go` (routes), `internal/web/templates/topic.html` and `forum.html` (buttons), `internal/web/csrf.go` (`maxPostingBody = 256 << 10`), `internal/web/sideeffect_test.go`, `internal/web/basepath_test.go`, `internal/web/static/` (CSS for the form), `docs/changelog.md`

**Interfaces:**
- Consumes: Task 2's functions and `board.InputError`; Task 1's `htmlcheck.Check`.
- Produces: the routes in "Decisions". The edit form is reused by Task 4's moderator edit.

Rules:
- The forum page shows "New topic" when the viewer may start one. The topic page shows "Reply" at the top and bottom, and "Quote" and "Edit" or "Delete" on each post, each only when the viewer may.
- "Delete" is a POST form with a confirm step that needs no script: the first POST answers with a page that asks "Delete this post?", holding a second form with `confirm=1`.
- The form shows the title field on a new topic, and on the edit of a first post. The text box holds the source. Under it, one line says "Markdown works here: **bold**, *italic*, [link](https://example.com), > quote."
- For a guest, the form adds a "Your name" field (see "Guest posts"). The "New topic" and "Reply" buttons show to a guest only where the guests group may post.
- On success: 303 to `/p/{id}`. On an `InputError`: 400 with the form, the text and the message beside the field. On `ErrForbidden`: 403. On `ErrNotFound`: 404.

- [ ] **Step 1: Write the failing tests**
  - `TestPostingFlow`: as alice, through `httptest`, with the CSRF token from the form:
    - GET the new-topic form;
    - POST a topic, and get 303;
    - follow it to the topic page, which shows the post;
    - reply with a quote of it, which shows a `blockquote` with "alice wrote";
    - edit the reply, which shows the edited text and an edit note;
    - delete the reply with the confirm step, which removes it from the page.
  - `TestQuoteHiddenPostIsNotFound`: `/t/{id}/reply?quote={post}` answers 404 for a post in a forum the viewer cannot read, and for another member's unapproved post. The body holds none of the post's text.
  - `TestFormKeepsTextOnError`: a POST with a 121-character title and a 2,000-word body answers 400. The form holds the full body and the message "The title is longer than 120 characters."
  - `TestPreviewSavesNothing`: a POST with `preview=1` answers 200 with the rendered body, and the post count does not change.
  - `TestPostingNeedsToken`: a POST without the CSRF token answers 403, and nothing changes.
  - `TestGuestPostingPage`:
    - with `topic.start` granted to the guests group on forum 2, a guest sees "New topic", fills in "Your name", posts and gets 303, and the topic page shows the name with no profile link;
    - a guest sees no "Edit" or "Delete" on that post;
    - on a forum without the grant, the guest sees no "New topic", and the form's URL answers 403.
  - `TestPayloadsThroughPosting`: each line of `markup/testdata/xss/payloads.txt` is posted as a reply. On each topic page that shows them, every `.post-body` element passes `htmlcheck.Check`. The page's CSP header is the same as for a page with no payloads.
  - `TestCSPStrictEverywhere`: for every GET path in `getPaths`, the new form pages included, the CSP has no `unsafe-inline`, no `unsafe-eval`, no `*` and no `data:` in `script-src`. Its `object-src` is `'none'`, its `base-uri` is `'none'`, and its `form-action` is `'self'`.
  - Add the form pages to `getPaths` and `TestBasePathEverywhere`.
- [ ] **Step 2: Run them to verify they fail.** Run: `GOBB_TEST_DATABASE_URL=… go test ./internal/web/`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: new topic, reply, quote, edit and delete"`

**Checkpoint:** on the private copy, the owner posts, quotes, edits and deletes through the browser, on a desktop and a phone.

---

### Task 4: Moderation in place

**Files:**
- Create: `internal/web/mod.go`, `internal/web/mod_test.go`
- Modify: `internal/web/site.go`, `internal/web/templates/topic.html`, `docs/changelog.md`

**Interfaces:**
- Consumes: Task 2's DeletePost, RestorePost, ApprovePost, EditPost and SetTopicLock; Task 3's edit form and confirm page.
- Produces: the moderator controls on the topic page.

Rules:
- A viewer with the right sees "Delete" or "Restore", and "Approve" on a held post, on each post. On the topic, the viewer sees "Lock" or "Unlock".
- A moderator's edit of another member's post shows an "Edit reason" field, and the reason appears in the edit note.
- A held post shows "Waiting for approval" to its author and to moderators. A deleted post shows "Deleted" to moderators only (Plan 3's rule).

- [ ] **Step 1: Write the failing tests**
  - `TestModeratorControls`: as bob, granted `mod.delete`, `mod.approve`, `mod.edit` and `mod.lock` on forum 2 by the test:
    - delete a reply, then restore it;
    - approve a held post;
    - lock the topic, then unlock it.
    - After each step the topic page and the index show the expected counters, and `mod_log` gains one row.
  - `TestNoModControlsWithoutRights`: carol sees none of the controls, and her POSTs to each answer 403 or 404.
  - `TestLockedTopicRefusesReply`: after the lock, alice's reply POST answers 403 with the form and "This topic is locked." bob's reply succeeds.
  - Add the moderator POSTs to `TestPostingNeedsToken`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/web/`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: delete, restore, approve and lock in place"`

**Checkpoint:** the owner, as admin on the private copy, deletes and restores a post, approves one of the two held posts, and locks and unlocks a topic.

---

### Task 5: The account page

**Files:**
- Create: `internal/board/account.go`, `internal/board/account_test.go`, `internal/web/account.go`, `internal/web/account_test.go`, `internal/web/templates/account.html`
- Modify: `internal/web/site.go`, `internal/session/session.go` (`DeleteUser`), `docs/changelog.md`

**Interfaces:**
- Consumes: `login.Service.Check`, `password.Hash` (the gobb scheme), `account.CleanEmail`, `session.Create`, and `board.ParseBody` (signature flags).
- Produces:
  - `session.DeleteUser(ctx, q, userID int64) (int64, error)`, which Task 6 also uses.
  - `board.ChangePassword(ctx, pool, v, next string, now time.Time) error`.
  - `board.ChangeEmail(ctx, pool, v, email string, now time.Time) error`.
  - `board.SetSignature(ctx, pool, v, src string, now time.Time) error`.
  - The handler checks the current password first, through `login.Service.Check` with the member's own username and the request's address. Only then does it call the board function. The board functions check the rights (`account.password`, `account.email`, `account.signature`) themselves.

Rules:
- `/account` shows three forms: password, email and signature. A guest still gets Plan 3's 302 to `/login`.
- **Password.**
  - It needs `account.password`.
  - The current password goes through `login.Service.Check` with the member's own username, so the limits, the gate and the floor apply.
  - The new password is stored with the gobb scheme, and `password_scheme` becomes `gobb`.
  - Every session of the member is deleted (spec 10). A new session is created for this browser, with its "remember me" choice kept.
- **Email.**
  - It needs `account.email` and the current password.
  - An address that another account already uses, compared with `account.CleanEmail`, is refused with "That address belongs to another account."
  - `email_verified` becomes false. Verification comes with email after cutover (spec 17).
- **Signature.**
  - It needs `account.signature`.
  - It runs `board.ParseBody` with `forumID` 0 and the member's stored signature flags, under the 2,000-byte limit.

- [ ] **Step 1: Write the failing tests**
  - `TestChangePassword`:
    - alice changes her password with the right current one;
    - her old session cookie then gets the guest page, and the new cookie in the answer is signed in;
    - the old password fails at `/login`, and the new one works;
    - a wrong current password answers 400 and changes nothing;
    - `next` of 9 characters, or equal to "alice", is refused.
  - `TestChangeEmail`: a taken address is refused. A free one is saved, with `email_verified` false. The email never appears on the profile.
  - `TestSetSignature`:
    - a signature with `**x**` shows bold on her posts;
    - 2,001 bytes are refused;
    - set as her signature, each line of the payload corpus gives a `.signature` element that passes `htmlcheck.Check` on her topic page.
  - Add `/account` to `getPaths` and `TestBasePathEverywhere`, and the three POSTs to `TestPostingNeedsToken`.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `GOBB_TEST_DATABASE_URL=… go test -race ./internal/...`. Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "web: change the password, the email address and the signature"`

**Checkpoint:** the owner changes the password of the made-up member on the private copy, and signs in again with the new one.

---

### Task 6: Admin commands

**Files:**
- Create: `cmd/gobb/user.go`, `cmd/gobb/user_test.go`, `internal/board/admin.go`, `internal/board/admin_test.go`
- Modify: `cmd/gobb/main.go` (the `user` command), `README.md`, `AGENTS.md` (the Commands block), `docs/changelog.md`

**Interfaces:**
- Consumes: `session.DeleteUser` (Task 5), `password.Hash`, `account.CleanUsername`, and the `bans` table.
- Produces:
  - `gobb user reset-password <name>`;
  - `gobb user ban <name> [-reason TEXT]`;
  - `gobb user unban <name>`;
  - `board.ResetPassword(ctx, pool, userID int64, now time.Time) (string, error)`;
  - `board.BanUser(ctx, pool, userID int64, reason string, now time.Time) error`;
  - `board.UnbanUser(ctx, pool, userID int64, now time.Time) error`;
  - `board.UserByName(ctx, q, name string) (int64, error)`.

Rules:
- `<name>` matches `username_clean` through `account.CleanUsername`. An unknown name exits 1 with "no member named <name>".
- **`reset-password`** makes a random password of 16 characters from an alphabet without look-alike characters. It stores the password with the gobb scheme, deletes the member's sessions, writes `mod_log`, and prints the new password once on stdout. It never logs the password.
- **`ban`** adds a `user` ban with no end date, deletes the member's sessions (roadmap, "Carried from Plan 3"), and writes `mod_log`. Sign-in already refuses a banned member (Plan 3).
- **`unban`** removes that member's `user` bans, and writes `mod_log`.

- [ ] **Step 1: Write the failing tests**
  - `TestResetPassword`:
    - the printed password signs alice in, and her old one fails;
    - her earlier sessions are gone;
    - one `mod_log` row has `{"via":"cli"}`;
    - the password does not appear in the logs that the command wrote.
  - `TestBanAndUnban`:
    - after `ban`, alice's live session answers as a guest, and a sign-in with her right password answers "banned";
    - after `unban`, the sign-in works;
    - two `mod_log` rows.
  - `TestUserCommandUnknownName`: exit 1, the message above, and no change.
- [ ] **Step 2: Run them to verify they fail.** Expected: FAIL.
- [ ] **Step 3: Implement.** Update the README section "Run the server" with the three commands, using `docker compose exec gobb gobb user …`. Run each command against the Compose stack before you commit it.
- [ ] **Step 4: Run them to verify they pass.** Expected: PASS, no skips.
- [ ] **Step 5: Commit and push.** `git commit -m "cmd: reset a password, and ban or unban a member"`

**Checkpoint:** the owner runs `gobb user reset-password` for the made-up member on the private copy, and signs in with the printed password.

---

### Security review (controller, after Task 6)

The controller dispatches a fresh Opus reviewer. Its whole scope is security, over the range from Task 1's base to Task 6's head. **(owner: security is paramount)** It checks:
- every path by which member text reaches a page;
- every POST, for its CSRF and Cross-Origin protection;
- every write, for its rights and for IDs changed in the URL or the form;
- the quote path, for leaks of hidden posts;
- the account changes, for session handling;
- the CLI, for passwords in logs or in shell history.

It may run the server and attack it with its own payloads. Its Critical and Important findings get one fix round, and a scoped re-review, before Task 7 starts.

---

### Task 7: Release, backup and upgrade

**Files:**
- Create: `.github/workflows/release.yml`, `scripts/releasecheck/main.go`, `scripts/releasecheck/main_test.go`
- Modify: `Dockerfile` (cross-compile with `--platform=$BUILDPLATFORM`, `TARGETOS` and `TARGETARCH`), `.github/workflows/ci.yml` (a backup round trip in the `image` job), `README.md`, `docs/changelog.md`

**Interfaces:**
- Consumes: `docs/changelog.md`, `internal/version`, and the `image` job of CI.
- Produces:
  - **A `workflow_dispatch` input `dry_run`** (default `true`). It builds every artifact and runs the smoke run on a local image, but pushes nothing and creates no release.
  - **The tag `v0.1.0`**, which publishes:
    - binaries for `linux/amd64`, `linux/arm64` and `darwin/arm64`, built with `CGO_ENABLED=0 -trimpath`, each with a SHA-256 sum;
    - the image `ghcr.io/evanwtf/gobb:0.1.0` and `:latest`, for `linux/amd64` and `linux/arm64`;
    - a GitHub release whose notes are the changelog's `## 0.1.0` section.
  - **`go run ./scripts/releasecheck <tag>`**, which exits non-zero when:
    - the tag is not `vX.Y.Z`;
    - the changelog's first `## ` section is not exactly `X.Y.Z` followed by a date in parentheses (it refuses "(unreleased)");
    - `internal/version` disagrees with the tag;
    - the section is empty.

Rules:
- The workflow runs on `[self-hosted, Linux, X64]`. The jobs run in this order: `releasecheck`, then the build, then pushing the image. Last, a smoke run of the pushed image, `docker run --rm ghcr.io/evanwtf/gobb:0.1.0 version`, must print `0.1.0`.
- The release notes come from the changelog file, never from the tag message.
- **Backup.** The README gains "Back up and upgrade", in this order:
  1. Run `pg_dump -Fc` through `docker compose exec -T postgres`, to a dated file on the host.
  2. Run `tar czf` of the data volume.
  3. Run `docker compose pull && docker compose up -d`, which applies new migrations at start.
  4. To restore: `pg_restore --clean --if-exists` and the tar file.

  The README says to keep the files on the server, and to check the dump's size before the upgrade.
- **CI round trip.** After the smoke run in the `image` job:
  1. Dump the database the way the README says.
  2. Restore it into a new database.
  3. Compare the row count of every table in `db.DataTables`.
  4. Fail on any difference.

- [ ] **Step 1: Write the failing tests.** `TestReleaseCheck` covers each refusal above with its own case, and one passing case. Run: `go test ./scripts/releasecheck/`. Expected: FAIL (the package does not exist).
- [ ] **Step 2: Implement the check, and make it pass.**
- [ ] **Step 3: Add the workflow, the Dockerfile change and the CI round trip.** Push to `main`. The `image` job must pass with the round trip, and the cross-compiled image must still smoke-run.
- [ ] **Step 4: Prove the release without publishing.**
  - Push the tag `v0.0.0-rc.1` on `main`. The workflow must stop at `releasecheck` (the tag is not `vX.Y.Z`, and the changelog says `0.1.0 (unreleased)`), and publish nothing. Delete the tag.
  - Run the workflow by hand with `dry_run` true. Expected: every job passes, and the registry and the releases page are unchanged.
- [ ] **Step 5: Commit and push.** `git commit -m "ci: release on a tag, and prove the backup round trip"`

**Checkpoint:** the controller runs the README's backup and restore commands against its Compose copy of the first board. The row counts match.

---

### Final review (controller)

Once Task 7 is green, the Opus reviewer reviews the whole plan's work against the spec sections in the header and the Review Focus. Then one fix dispatch, and one scoped re-review.

---

## Cutover (controller and owner)

These steps run on the production host, from the owner's private deployment notes. They name no host in this repo. Each step needs the owner's go-ahead at the time.

1. **Converter.** Close the converter's open items in the private repo, and build a bundle from a fresh dump.
2. **Release.** Set the changelog's `## 0.1.0` date, push, tag `v0.1.0`, and watch the release workflow.
3. **Rehearsal (spec 18, steps 1–2).** Under Docker Compose on the production host, with the published image and the board's current base path:
   - import the bundle, time the import, and restart gobb; check that its start-up lines give the expected charge and floor;
   - check the forwarded headers and the CSP through an SSH tunnel;
   - check all three password conditions: every imported hash is recognized (a count); the legacy test vectors pass in CI; and the owner signs in with a real account.

   The public proxy stays on the old board.
4. **Cutover (spec 18, steps 3–6).**
   - Put the old board in maintenance mode, take the final dump, build the bundle and import it.
   - Restart gobb after the import. It reads the stored hashes' cost and times the sign-in floor at start, so a server that started on an empty database charges too little and floors too short.
   - Back up the new database (Task 7).
   - Point the proxy at gobb.
   - Smoke-test reading, sign-in, posting and an old URL.
   - The owner signs in with a real account.
5. **Done when (owner):**
   - gobb serves the board's website;
   - the old software's process no longer runs on the host;
   - every member account signs in with its existing password.

   The old files and the final dump stay for the rollback window. Removing the old software (spec 18, step 7) follows at the end of that window.

## Order

The tasks run in order, one at a time. Task 1 has no dependency on the others, so it may start at once. Tasks 3 and 4 need Task 2. Task 6 needs Task 5's `session.DeleteUser`. The security review comes after Task 6, and Task 7 after the review's fixes.
