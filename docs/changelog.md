# Changelog

## 0.1.0 (unreleased)

Plan 1, the foundation. gobb has no forum pages yet; this release ships the pieces
that the pages stand on.

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
- Login limits. Ten failed sign-ins from one address in 15 minutes, or 20 for one name in an hour (the name is cleaned, and counts whether or not an account has it), make the next try answer 429 with `Retry-After` and "Too many sign-in attempts. Try again in N minutes." on the sign-in page, with the form still there; N rounds up, so it never reads "0 minutes". A limited try runs before any lookup or hash and does not wait for the 400 ms floor; a good password clears the name's count. Counts live in memory (`internal/ratelimit`, at most 10,000 keys for each limit) and start again at zero when the server restarts. Password checks share a 128 MiB gate (`login.Gate`): each check holds the memory its hash needs (`password.MemoryKiB`: the `m` of an argon2id hash, the costliest layer of a chain, 1,024 KiB for anything else), the dummy check for an unknown name goes through the same gate, a check that cannot get room in 5 seconds answers 503 with `Retry-After: 5`, and the 400 ms floor is waited after the gate is free. Request bodies are capped before the CSRF check: 16 KiB for `POST /login`, 4 KiB for every other POST, and a larger body answers 413 in the layout. `gobb serve` deletes expired sessions and `user_ips` rows last seen more than 90 days ago once at start and then every hour (`session.Cleanup`), and logs the two counts.
