# gobb: design

Date: 2026-10-23. Status: waiting for the owner's approval.

Labels used below:

- **(owner)** — the owner decided this in the design conversation.
- **(designer's call)** — the designer chose this; the owner can overturn it.
- Anything unlabeled follows from the data or from the goals.

## 1. Intent

gobb is a forum server in Go: one binary, PostgreSQL, server-rendered pages. It is built
first for one existing community board (the "first board"), which moves onto gobb from
its old forum software through an import bundle (section 8). **(owner)**

Why:

- The first board's old software is a large, aging codebase that stays safe only with
  constant updates. gobb aims for few dependencies and little to patch.
- Sign-up on the first board is closed because spam accounts overran it: about 10,000
  of 10,300 accounts are inactive, and about 2,600 sign-up attempts arrived in two weeks.
  With its own code, the board can put spam defenses where they help and reopen sign-up.
  **(owner)**

Scope: built first for the first board's needs, and usable by any board: a new board,
or one that moves from other software through a bundle. Numbers that suit the first
board are defaults that another board can change, not fixed values. **(owner)** The
repo is public under the MIT license. **(owner)**

**Clean room. (owner)** gobb is written from this document, its plans and its own tests.
It contains no code, templates, stylesheets or images from any other forum software, and
nobody who works on it reads such source. It keeps the basic look and the behavior that
members rely on, and nothing more. The converter that builds the first board's bundle
needs to know the old system, so it lives with the private deployment, outside this
repo.

## 2. The first board

Sizes from a dump taken 2026-10-23. The import and the pages must handle them.

| Item | Count |
|---|---|
| Posts / topics | 31,759 / 2,194 |
| Post visibility | 31,757 approved, 2 waiting for approval, 0 deleted |
| Posts with markup off / smilies off / URL linking off | 458 / 459 / 3 |
| Topic kinds | 2,187 normal, 3 sticky, 3 announcement, 1 global; 1 moved-topic marker |
| Forums | 16, in 3 categories, nested up to 2 levels |
| Members to import | 204 (the inactive spam accounts are not imported) |
| Guest posts | 24 |
| Groups | 24 |
| Grants | about 500, with NEVER at group level, at user level and inside roles |
| Private messages | 519 (3 in the last year) |
| Polls | 19 (the last one in 2019) |
| Attachments | 322 image files, about 172 MB |
| Avatars | 39 |
| Bans | 125: 84 IP, 40 email, 1 user; all exact, none with an end date |
| Smilies / ranks | 42 / 30 |
| Posts in 2026 | 51, by about 10 people |
| Production host | 1 GB of RAM and 2 GB of swap |

## 3. Who it serves

- **Occasional members.** They drop in about once a month to see what is new. **(owner)**
  Their key screen is "new since your last visit".
- **Archive readers and crawlers.** Fourteen years of posts, with links all over the web.
- **New members,** once sign-up reopens.
- **One admin.** Day to day the admin deletes posts and bans people. **(owner)**

## 4. Goals

1. Members can do about 99% of what they do on the first board today (section 7).
   **(owner)**
2. Every post, topic, private message, poll, attachment and avatar comes over. Forum,
   topic, post and user IDs stay the same.
3. Members log in with their current passwords (section 10).
4. Groups and permissions give the same result as today, NEVER included. **(owner)**
5. Old URLs answer with a 301 to the matching new page (section 14).
6. Email works, and sign-up reopens behind spam defenses (release 2; section 17).
7. A small admin panel that is much better than the old one. **(owner)**
8. One opinionated, good-looking design. **(owner)**

## 5. Non-goals

- Databases other than PostgreSQL.
- Plugins, extensions, uploadable themes or templates, a theme editor, language packs.
  English only. **(owner,** on themes)
- Warnings, karma, friends and foes, PM folders and rules, drafts, post reports, custom
  profile fields, OAuth login, Jabber, feeds, birthdays, age checks. **(designer's call:**
  each is unused or nearly so on the first board.)
- A JSON API, real-time updates, a mobile app.
- Converters for other forum software in this repo. Any converter can write a bundle.
- Several gobb processes on one database. Sign-in counts, Defensive Mode and the
  guest page cache live in the memory of one process. **(designer's call)**

## 6. Anti-goals

The build fails, even if it runs, if any of these happen:

- A post, private message, poll vote or attachment is lost or shown under the wrong
  author.
- A member cannot log in with the password they use today.
- Someone can read a forum that their permissions hide from them.
- An old link that works today returns 404. (One exception: profiles of accounts that
  were not imported return 410; section 14.)
- Sign-up reopens and spam floods in again.
- Any path lets post text, a signature, a profile field or an embed emit unsanitized HTML or
  script.
- The dependency tree needs constant patching. Keep dependencies few.
- Code, text or images copied from another forum product. **(owner)**

## 7. Features at launch

**Reading**

- Board index, forum pages, topic pages with pagination, member profiles, member list,
  "the team" page.
- "New since your last visit". It uses imported read-tracking data, so it is right on
  day one.
- Search over posts (title and body), with filters for forum and author.
- Restricted forums stay hidden from members who lack the right.

**Accounts**

- Register and verify email (switched on in release 2; section 17), log in with
  username or email **(designer's call)**, log out, reset password by email.
- Edit profile: email (re-verified), password, avatar (upload), signature, time zone,
  date format, color scheme, and "About you": location, occupation, interests, website
  and birthday.

**Posting**

- New topic, reply, quote, edit own post, delete own post (as permissions allow).
- Markup toolbar, preview, smilies.
- Attach images.
- Vote in polls. Create polls (permission `poll.start`).

**Private messages**

- Inbox and sent; read the imported messages; send, reply and quote; multiple
  recipients.

**Notifications**

- Watch a topic (auto-watch on posting, as an account setting).
- Email on a new reply: one email per topic until the member visits that topic again.
- Email on a new private message.
- An unread-PM count in the header. There is no notification center.

**Moderation and admin:** section 11.

**Static pages:** terms, privacy, FAQ. The admin edits them as text.

## 8. Import

### 8.1 The bundle

Data comes into gobb as a bundle: a directory, or a `.tar.gz` of one. **(designer's
call)** The format is gobb's own and is documented in this repo, so any converter can
write it.

- `manifest.json`: the format name `gobb-bundle`, the format version (1), the creation
  time, a free-text source label, and the row count of every data file.
- One JSON Lines file per entity: users, gone users, groups, group members, roles,
  grants, forums, topics, posts, polls, poll votes, attachments, private messages,
  watches, bookmarks, read marks, bans, ranks, smilies, legacy routes,
  legacy file paths, static pages, member addresses (section 10, "Trusted
  addresses"). Board settings are in `settings.json`.
- `files/`: attachments, avatars, smilies, rank images, the header image. Every row that
  names a file gives its path under `files/`, its size and its SHA-256.

Rules:

- A bundle's IDs become gobb's IDs. Rows refer to each other by ID.
- Times are RFC 3339 with an offset. Text is UTF-8.
- A post, signature or private message carries its Markdown source and its flags
  (Markdown on, smilies on, URL linking on, signature shown). gobb parses the source.
  A converter from another markup writes Markdown and checks it with the `markup`
  package before it writes the bundle (section 8.3).
- A password carries a scheme and a hash (section 10).
- Gone users are IDs that the source had but the bundle does not import. Their profile
  URLs answer 410.
- Unknown fields, unknown enum values, broken references,
  and a count that disagrees with the manifest all stop the import. gobb does not guess
  at data it does not model.

Plan 2 fixes every file and field.

### 8.2 `gobb import`

- Loads one bundle into an empty gobb database, in one transaction. It refuses a
  non-empty database unless told to replace it.
- Copies the files into the data directory after it checks each size and SHA-256.
- Parses and renders every post, signature and private message once, and checks each
  tree against the node schema (section 12.1). Any error stops the import.
- Prints the count loaded for each entity.

### 8.3 Checks that need the old system

The converter owns every check that needs the old system's data or rules. For the first
board those checks are private: exact permission equivalence against the old rules, a
parse comparison for every post, the old URLs from the access logs, and a login test on
a rehearsal copy. gobb gives the converter two tools:

- `gobb perms export` writes each user's effective permissions as JSON Lines.
- The `markup` package is public, so a converter can parse the Markdown it writes and
  `Compare` the tree with the one it expects.

The public repo tests the importer with hand-written bundles only.

## 9. Groups and permissions

### 9.1 Model **(designer's call)**

- **Options** name single rights (table below).
- **Roles** bundle option settings.
- **Grants** give a role, or single option settings, to a group or a user, per forum or
  board-wide.
- A setting is YES, NO or NEVER. For a user, gobb combines the user's own grants and
  the grants of every group the user belongs to (pending memberships excluded). Any
  NEVER gives NO. Otherwise any YES gives YES. Otherwise NO.
- Forum options apply per forum. Board options apply board-wide; grants on a forum do
  not change them. Moderator options exist at both levels: the result in a forum is the
  board-wide result OR the forum result.
- **Owners** are the accounts that run the board. They always have `admin`. Their forum
  rights still come from grants.

| Scope | Options |
|---|---|
| Forum | `forum.list` (see it listed), `forum.read`, `forum.search`, `topic.start`, `topic.reply`, `topic.sticky`, `topic.announce`, `topic.global`, `topic.lock_own`, `topic.watch`, `post.edit_own`, `post.delete_own`, `post.markdown`, `post.smilies`, `post.images`, `post.signatures`, `post.no_approval`, `post.counts` (counts toward the member's post count), `attach.upload`, `attach.download`, `poll.start`, `poll.vote`, `poll.change_vote` |
| Moderator | `mod.edit`, `mod.delete`, `mod.approve`, `mod.lock`, `mod.move`, `mod.view_ip` |
| Board | `mod.ban`, `pm.send`, `pm.read`, `pm.send_many`, `pm.markdown`, `pm.smilies`, `pm.images`, `pm.edit`, `pm.delete`, `account.signature`, `account.avatar`, `account.profile`, `account.email`, `account.password`, `account.attach`, `account.download`, `account.search`, `account.view_profiles`, `admin` |

An upload needs both `account.attach` and the forum's `attach.upload`. A download needs
both `account.download` and `attach.download`. Search needs `account.search`, and shows
results only from forums with `forum.search` and `forum.read`.

Built-in groups: `guests`, `members`, `moderators`, `admins`. Bundle grants for them
replace gobb's defaults. A new sign-up joins `members`.

### 9.2 Admin rights

The admin pages and the log need the single `admin` right.

## 10. Accounts and spam defense

**Passwords**

Two schemes:

- **`gobb`:** an argon2id PHC string over the password in Unicode NFC. Every new or
  changed password uses it.
- **`legacy`:** hashes from the first board's old software. Before it checks a legacy
  hash, gobb turns the password into its *legacy form*, in this order:
  1. Replace CR LF and a lone CR with LF. Remove NUL and U+FEFF.
  2. Escape `&` as `&amp;`, `"` as `&quot;`, `<` as `&lt;` and `>` as `&gt;`. Single
     quotes stay. Invalid UTF-8 never matches.
  3. Normalize to Unicode NFC.
  4. Remove space, tab, LF, CR, NUL and vertical tab from both ends.

| Legacy format | Check |
|---|---|
| `$2y$`, `$2a$`, `$2b$` | bcrypt |
| `$argon2id$` | argon2id PHC string (`m`, `t`, `p` from the string) |
| `$chain$phpass,` + 9 characters + a bcrypt hash | Two steps. (1) The phpass portable hash (public domain) of the input, with the 9 characters as its setting (a count character and an 8-character salt); keep its 22-character encoded digest. (2) Check that digest against the bcrypt hash. The input is the legacy form, or the lowercase hex MD5 of the legacy form; either may match. |
| empty | never matches |

- Test vectors (made-up passwords, their hashes, and the old software's own verdicts)
  come from the private converter tooling, which runs the old software in a throwaway
  container. The repo holds the vectors file only.
- A password over 4,096 bytes never matches, and gobb does not hash it.
- After a successful legacy login, gobb stores a `gobb` hash and switches the account's
  scheme. **(designer's call)**

**Login**

- Username or email. If an email belongs to more than one account (2 addresses on the
  first board), ask for the username. **(designer's call)**
- Login and password-reset responses do not reveal whether an account exists.
  Registration does say "username taken"; it cannot avoid that.

**Sessions** (designer's calls throughout)

- Server-side sessions; secure, HttpOnly, SameSite=Lax cookie.
- "Remember me" lasts a year, because members visit about monthly.
- A password change, a password reset, a ban or the Spammer action ends all of that
  user's sessions.
- A CSRF token on every state-changing request.
- Sign-in limits: see "Load protection" below.
- "Last visit" updates when a new session starts.
- Store each session's IP and user agent. Record each address that a member uses while
  signed in, at sign-in and on later visits, and keep it for one year after its last
  use. A "remember me" session slides on each visit, so a monthly member may never
  sign in again; recording visits keeps their current address known. These addresses
  are the member's *trusted addresses* (below), and the Spammer action uses them.

**Load protection** (owner: the sign-in schedule, Defensive Mode's trigger and
penalties, and the import of old addresses; the rest are designer's calls)

The rule: gobb never holds a scarce resource (a database connection, a password
check, memory) while a request waits for it. A request that cannot get one soon gets
an immediate 429 or 503 with `Retry-After`.

- **Sign-in schedule.**
  - Each failed sign-in counts against the client's address: an IPv4 address, or an
    IPv6 /64. It also counts against the account name, unless the address is one of
    that member's trusted addresses. An attacker who hammers a name therefore cannot
    lock the member out of their own address.
  - Two failures are free. After that, the next try must wait 15 s, 15 s, 60 s, 60 s
    and 60 s, then 900 s before every later try.
  - A try made before its wait ends gets an immediate 429 with "Try again in N
    seconds". gobb does not check the password, and the count does not advance.
  - A good sign-in clears the name's count. It does not clear the address's count,
    so one account of one's own cannot reset an address that guesses at other names.
    A count clears after 24 hours with no failure.
  - This schedule replaces any fixed per-address or per-name window. The equal-time
    floor (a failed sign-in takes the same time for every name) and the memory gate
    on password checks stay.
- **Settings.** Every number in this block is a setting with a default, and so is
  the time that gobb keeps an address. The guest page cache and its start-up render
  can each be turned off. The defaults suit a small board on a 1 GB host. Settings
  come from the environment (section 15), which Docker Compose reads from `.env`.
- **Trusted addresses.** The addresses a member used while signed in (above). An
  address is trusted only through a member in good standing: active, not banned, and
  out of the holding area. A ban or the Spammer action removes that member's
  addresses. So a spam account cannot make its own addresses trusted. A bundle may
  carry each member's past addresses. The file is optional, and a new board starts
  with none. The first board's import loads all of its members' addresses, whatever their
  age, and keeps each one for one year after the import. **(owner)**
- **Defensive Mode.**
  - It turns on when the board counts more than 100 failed sign-ins in 60 s, from
    all addresses together. **(owner)** The builder may also turn it on when requests
    wait too long for a database connection.
  - It turns off after 10 minutes below that rate.
  - While it is on, an address that is no member's trusted address gets no free
    tries, and waits at least 5 s between tries. **(owner)** A trusted address keeps
    the normal schedule.
  - While it is on, guests cannot search, and each untrusted address has a tighter
    request budget for every page.
  - It writes one log line when it turns on and one when it turns off, with counts
    and no addresses.
- **Database connections.**
  - The pool size is a setting, with a default that suits the 1 GB host.
  - A request waits at most about 1 s for a connection, then gets 503 with
    `Retry-After`.
  - Search has its own limits, apart from every other page. The whole board runs at
    most 5 searches per second, from all addresses together, a setting with that
    default. There is no limit per address or per member. **(owner)** A search over the
    limit gets an immediate 429, and does not touch the database. Under a flood,
    search stops for everyone, but the pages stay up.
  - The 429 is a normal page of the board, in its layout, with a `Retry-After`
    header, that says "Search functionality is currently disabled." **(owner)** A
    guest who searches while Defensive Mode is on gets the same page.
- **Guest page cache.**
  - A page that a guest can see is rendered once and served from memory.
  - At start, a background worker renders the pages most likely to be asked for, in
    this order, until the cache's memory limit: the index, the first page of each
    forum, then topic pages by latest activity. On a small board that is every guest
    page. (The first board's guests can see about 160 pages.)
  - A change that alters a page drops it, together with the pages that list it (its
    forum's pages and the index). The worker then renders it again. A change to guest
    permissions drops the whole cache.
  - The cache key is the canonical path and the query parameters that the page
    accepts. A request with any other parameter does not reach the cache, so a
    random query string cannot force a render.
  - When several requests miss the same page at once, one renders it and the others
    wait for that render.
  - The per-visitor CSRF token is filled in when the page is served. It is never
    stored in the cache.
  - The cache has a memory limit. Over the limit, the least recently used pages go.
  - Members always get live pages.
- **Batched writes.** View counts, last-visit times and the addresses of signed-in
  visits collect in memory. They go to the database in one batch about every 30 s, so
  a page view does not write. A crash loses at most that window.

**Email** (designer's calls, except the sending account — see the private deployment
notes)

- SMTP with STARTTLS or implicit TLS, and optional auth.
- A queue in the database with retries and backoff. A failed send never blocks a
  request. Failures show in the admin log.
- Verification links are single-use and last 48 hours; reset links last 1 hour.
- A member can ask for a new verification email. The admin can verify an account by
  hand.
- A board setting turns reply and PM notifications on or off. It starts off at
  cutover; the admin turns it on after a test send works.

**Registration** (all built in; nothing third-party by default — designer's call)

- Admin setting: closed, open, or open with admin approval.
- A hidden honeypot field and a minimum time between page load and submit.
- Rate limits per IP and per IP range.
- A question-and-answer challenge; the admin writes the questions.
- Email verification before the account can post.
- Bans on email domains and IP ranges apply at sign-up.
- An optional hosted captcha (for example Cloudflare Turnstile) behind a setting, off by
  default.

**Holding area**

- A new member's first posts wait for approval before others see them. The number is a
  setting.
- The author sees a held post, marked as waiting, and can edit or delete it.
- While a member is in the holding area, an edit to any of their posts sends that post
  back to the queue. After they leave it, edits are not re-checked. **(designer's
  call)**
- The admin approves or rejects from a queue, or uses "Spammer" (section 11).
- A member leaves the holding area only through approved posts, never through time.
  **(owner)**

**Trust** (owner: the premise, the sleeper-account rule and the gates; the rest are
designer's calls)

gobb assumes that a request with no signals comes from a malicious bot. **(owner)**
Each request earns a *trust band* from signals that a bot cannot fake cheaply.

- **Signals that raise trust:**
  - signed in;
  - a member in good standing: active, not banned, out of the holding area;
  - earned history: approved posts spread over separate weeks, replies from
    established members, and no posts removed by a moderator;
  - one of that member's trusted addresses (section 10, "Load protection").
- **Signals that lower trust:** failed sign-ins from the address, a high request rate,
  and an address in a banned range.
- **Time alone earns nothing. (owner)** An account that registers and waits earns no
  trust, however long it waits. This stops sleeper accounts: many accounts registered
  at once, left dormant, then used together for a flood.
- Signals that cost a bot nothing, such as the browser name or the age of a cookie,
  count for nothing.
- The import's members start as established. The import drops the spam accounts, so
  every imported member is a real person.
- Points add up inside the server, and the result is one of a few bands. Each band
  carries its own budgets (section 10, "Load protection") and its own rights. Bands
  keep the answer to "why was I slowed down?" plain and testable.
- Every signal comes from memory: the session, the trusted-address set, and counts
  loaded at sign-in. Scoring a request makes no database query.

**Gates.** Until a member earns trust: **(owner)**
- no links or embeds in posts;
- no private messages;
- no website, signature links or avatar in the profile;
- one new topic, so a returning member can say "I found you!" Otherwise the member
  only replies. Every post still goes through the holding area.

**Posting flood.** A burst of posts from the low bands sends every low-band post to the
queue, and tells the admin by email. It is Defensive Mode for posting.

**Wave cleanup.** The admin can remove, in one action, every account registered in a
time window that has no approved posts.

## 11. Moderation and admin panel

**In place, on any post** (for those with the rights): delete (soft; restorable), edit,
move topic, lock topic, approve.

**Spammer action** on a user: ban the account, its email and its last IPs, and delete
all its posts and PMs. One click with a confirm. **(owner:** "deleting posts, banning
people") Details **(designer's calls)**:

- IP bans are exact: each address the account used in the last 30 days (IPv4 /32,
  IPv6 /64). No ranges, so a shared address bans only what it must.
- Deletes are soft. A topic the spammer started is hidden with its replies. Their PMs
  disappear from recipients' inboxes. Replies by others elsewhere stay.
- The log entry has an Undo for 30 days that restores everything and lifts the bans.

**Who sees what**

- Moderation actions appear in place for anyone with the matching `mod.*` right in that
  forum (or board-wide). The approval queue shows them the held posts in their forums.
- The admin pages and the log need `admin` (section 9.2).

**Admin pages**

- **New members:** recent sign-ups with IP, email, time to submit and post count;
  approve, delete, Spammer.
- **Approval queue:** held posts.
- **Bans:** by username, email or IP range; reason; optional expiry.
- **Settings:** board name and description, sign-up mode, registration questions,
  holding-area size, email settings test, default color scheme, header image upload.
- **Permissions:** for each forum, a grid of groups against roles. A list of per-user
  grants. A role editor for the options inside a role.
- **Groups:** members, leaders, join requests, group type.
- **Forums:** create, edit, reorder, nest.
- **Smilies.**
- **Log:** every moderator and admin action, with who, what, when.

## 12. Content rendering

### 12.1 Markdown and the tree

- Members write Markdown. There is no BBCode. **(owner)** The syntax is CommonMark,
  with the changes below.
- A line break in the source is a line break on the page, as in chat apps.
  **(designer's call)**
- Bare `http://`, `https://` and `www.` addresses, and bare email addresses, become
  links. Link text longer than 55 characters shows the first 39, then ` ... `, then the
  last 10.
- `||text||` hides text until the reader clicks it. **(owner:** Discord's syntax.) A
  spoiler is inline: it can hold text, emphasis, links and images, and it can cross a
  line break, but not a paragraph break.
- A bare link to a supported site, alone on its line, becomes an embed (section 12.2).
  **(owner)** A link inside a sentence, or a link with its own text, stays a link.
- Smiley codes, such as `:)`, show the admin's smiley images. A code matches only at a
  word boundary, and never inside code or a link.
- Raw HTML in the source shows as text. **(designer's call)**
- No underline, color, size or other styling. **(owner)**
- Each post, signature and private message has three flags: Markdown on, smilies on,
  URL linking on. With Markdown off, the source shows as plain text with its line
  breaks. Embeds need URL linking on. The posting form offers the flags.
- The quote button inserts a block quote. Its first line names the author and links to
  the quoted post.
- An attachment shows inline where the source has a Markdown image whose URL is the
  attachment's URL. The page lists the post's other attachments below it.
- gobb parses source into a tree: a root, text nodes, and elements with a name and
  string attributes. The tree is stored as JSON with each post, signature and private
  message, and pages render from it.
- The node schema (element names, allowed attributes and their value patterns, which
  children an element may hold, nesting caps) is part of gobb.
- The renderer builds HTML from an allowlist of elements and attributes. Link URLs:
  `http`, `https`, `mailto`, or a path on this site. Image URLs: `http`, `https`, or a
  path on this site.
- The parser is goldmark, a CommonMark library for Go (MIT license, no dependencies of
  its own). **(designer's call:** CommonMark has hundreds of edge cases, and a tested
  parser makes posts behave the way members expect from other sites.)

### 12.2 Elements

| Element | Markdown | Output |
|---|---|---|
| `p`, `br` | a paragraph, a line break | a paragraph, a line break |
| `h` | `#` to `######` | a heading, scaled down to fit inside a post |
| `hr` | `---` | a horizontal rule |
| `quote` | `>` | a quote block |
| `list`, `li` | `-`, `*`, `+`, `1.` | a bulleted or numbered list |
| `pre` | a fenced or indented code block | preformatted; no smilies or links inside |
| `code` | `` `code` `` | inline code |
| `b`, `i` | `**bold**`, `*italic*` | bold, italic |
| `url`, `email` | `[text](url)`, `<url>`, a bare address | a link with `rel="nofollow ugc noopener"` |
| `img` | `![alt](url)` | a hotlinked image; `http://` becomes `https://` |
| `spoiler` | `\|\|text\|\|` | hidden text, shown on click |
| `smiley` | a smiley code | the smiley's image |

Embeds **(owner:** YouTube, X, Instagram, Reddit "etc."; the other sites are the
designer's call):

| Element | Link | Output |
|---|---|---|
| `youtube` | a YouTube video, short or live stream | a privacy-enhanced player |
| `vimeo` | a Vimeo video | Vimeo's player |
| `tweet` | a post on X | the post, through X's embed script |
| `instagram` | an Instagram post or reel | the post, through Instagram's embed script |
| `reddit` | a Reddit post | a plain link: "View post on Reddit" (no script, no frame) |
| `tiktok` | a TikTok video | TikTok's player |
| `threads` | a Threads post | the post, through Threads' embed script |
| `spotify` | a Spotify track, album, playlist, episode or show | Spotify's player |
| `soundcloud` | a SoundCloud track or playlist | SoundCloud's player |

- To add a site later, add one entry to the embed table: its link patterns, its output,
  and the origins it needs in the Content-Security-Policy.
- A bare link alone on its line to any other site shows a preview card: the title,
  description and image from the page's Open Graph tags. It is on by default.
  **(owner)** The server fetches the page, so the fetcher reaches public addresses
  only, follows few redirects, and stops at time and size limits. The post stores only
  the link; gobb stores the fetched card apart from it and refreshes it. The same
  fetcher resolves short links (for example TikTok and Reddit short links) to their
  embed. **(designer's call:** the guards and the storage.)
- Embeds for services that no longer exist are the converter's business: it rewrites
  them as plain links.
- Remote images (about 1,600 posts) stay hotlinked. No image proxy at launch: it would
  be a large feature with server-side request risk. **(designer's call)**
- A strict Content-Security-Policy, set per response. Each embed adds only the frame
  and script origins it needs, and only on pages that contain it. No inline script.

**Uploads** (attachments, avatars, header image; designer's calls)

- Accept PNG, JPEG, GIF and WebP only, checked by content, not by name. No SVG.
- Limits on bytes and on pixel dimensions, checked before decoding the full image.
- Avatars are resized and re-encoded to a static image. Attachments and the header
  image are stored as uploaded, so animated GIFs keep moving.
- Serve uploads with the verified content type and `X-Content-Type-Options: nosniff`.

## 13. Look

- One design. **(owner)** It keeps the classic board layout that members know: category
  bars, forum rows with topic and post counts and the last post, and topic pages with
  the author on the left. gobb's HTML and CSS are written from scratch. **(owner:** basic
  look kept, clean room)
- Server-rendered HTML that works without JavaScript; a little JavaScript for quote
  insertion, preview and collapsible blocks. It works on phones.
- Color schemes are built in and members pick one in their profile. **(owner)** The
  admin sets the default. No uploads, no editor. **(owner)**
- Every error page, 4xx and 5xx, uses the board's layout and default scheme. This
  holds when the database is down or slow, and when a page template fails. Plain text
  never reaches a visitor. **(owner)** Only `/healthz`, which machines read, answers in
  plain text.
- Every page ends its footer with one small line. A 2xx answer to a GET or HEAD
  request shows the time to everyone: `Rendered in 0.02 s · gobb 0.1.0 · build
  0f0f0fac`. An admin (the board-wide admin right) also sees the count of database
  queries: `Rendered in 0.02 s with 5 queries · gobb 0.1.0 · build 0f0f0fac`. Every
  answer to a POST, and every answer that is not 2xx, shows `gobb 0.1.0 · build
  0f0f0fac` only. The line holds nothing about the host, the viewer or the data.
  **(owner, 2026-10-25)** (count: admins only, controller's ruling, a leak through the
  sign-in page)
- **Slate** is the default. It comes from the first board's original header image:

| Role | Color |
|---|---|
| Header and brand | `#2e425b` slate navy |
| Accent (scrawl) | `#009aff` azure |
| Secondary | `#596d86` muted slate |
| "New" badge only | `#ff0000` red |

  The Slate page uses `#233346` for the page, `#2a3c52` for rows, `#596d86` for
  category bars, `#4fc0ff` for links on dark rows, and `#dbe3ec` for text. **(owner
  chose this "B2 Slate" mock.)**
- Two more schemes: **Slate Deep** (near-black navy page, header blue for bars) and
  **Slate Light** (light page, same navy and azure). **(designer's call)**
- Every scheme must pass WCAG AA contrast for body text and links.
- The admin can replace the header image (PNG, GIF including animated, JPEG, WebP).
  **(owner:** "header graphics")

## 14. URLs

- New pages get clean URLs that contain the preserved IDs. The exact shapes are the
  builder's.
- **Legacy routes** come from the bundle. Each maps an old path, plus query parameters
  that must have given values, to a gobb page: home, forum, topic, post, user,
  member list, team, attachment, avatar, search, account, or a static page. The target's ID comes from a
  named query parameter, or is fixed in the route. An offset parameter picks the page of
  a forum or topic. A post target goes to the topic page that holds the post, with the
  anchor `#p<id>`. Other query parameters are ignored. The answer is a 301.
- **Legacy file paths** come from the bundle. Each maps an old path prefix to one of
  gobb's file directories (smilies, ranks), since old posts and other sites link to
  those images directly.
- A moved-topic marker redirects to the topic.
- The permission check runs before any redirect. A target the visitor cannot see gets
  the same 404 as a missing one, so a redirect never reveals a hidden topic.
- A gone user's profile returns 410 Gone. This is the one accepted exception to the "no
  new 404s" anti-goal. **(designer's call)**
- The base path is a setting, so the board can stay under its current path.
- Guests see times in the board time zone. **(designer's call:** `America/New_York` for
  the first board.)

## 15. Architecture

- Go (latest stable), one binary; templates, CSS and JavaScript embedded.
  **(owner:** single Go binary)
- Two packages are public, for converters: `markup` (Markdown parser, tree,
  renderer, comparison) and `bundle` (the import format). Everything else is under `internal/`.
- PostgreSQL 16 or later, in gobb's own schema (its own table design, in its own database). **(owner)** Numbered SQL migrations,
  embedded, never edited after release.
- Attachments, avatars and the header image live on disk under a data directory.
- Search uses PostgreSQL full-text search. **(designer's call)**
- Runs behind a reverse proxy that terminates TLS. gobb trusts `X-Forwarded-For` only
  from configured proxy addresses (`GOBB_TRUSTED_PROXIES`, a list of CIDRs), because IP
  bans depend on it. Inside a container the proxy's connections arrive from the
  container network's gateway, not from 127.0.0.1, so the compose file sets this.
- **Deployable with Docker Compose. (owner)** The repo ships a `Dockerfile` and a
  `compose.yaml` with two services, each built or pulled from an image registry
  **(owner)**:
  - `gobb` names both its GitHub Container Registry image (`ghcr.io/evanwtf/gobb`, tag
    from `GOBB_VERSION`) and the local build context. `docker compose pull && docker
    compose up -d` runs the published image; `docker compose up -d --build` builds it
    from source.
  - `postgres` is always pulled.
  - The image builds on Chainguard: build stage `cgr.dev/chainguard/go`, runtime
    `cgr.dev/chainguard/static`, user `nonroot`, CGO off. The data directory is a volume
    owned by `nonroot`.
  - The PostgreSQL service uses the official `postgres:18` image **(owner, 2026-10-24)**, pinned to the major
    version, because a data directory cannot cross major versions unattended. This
    reason covers only this service.
  - The release binaries remain for anyone who runs gobb without Docker.
- Configuration from environment variables; secrets from the environment.
- Logs go to stdout with `log/slog`.
- Background work (email queue, cleanup) runs inside the process. No cron entry point.
- Few dependencies: a PostgreSQL driver, `golang.org/x/crypto`, `golang.org/x/text`
  and goldmark (section 12.1) are expected. Each additional one needs a reason in the changelog.

## 16. Build, test, release

- CI on GitHub Actions, on the self-hosted Linux runner, with a guard that keeps pull
  requests from forks off that runner. Steps: `gofmt` check, `go vet`, `staticcheck`,
  `govulncheck`, `go test -race`, integration tests against PostgreSQL.
- A guard in CI and in pre-commit refuses private network addresses, home-directory
  paths, and a private list of identifying words (kept as a repository secret). A hit
  prints the file and line, never the word.
- Test-first for the renderer, the importer, password verification and permission
  resolution. Fixtures are hand-written; no real forum data.
- CI builds the exact production image on every push and runs it
  (`docker run --rm <image> version`). Building alone does not prove the entry point
  boots.
- Releases on a `v*` tag: binaries for Linux and macOS on amd64 and arm64, with
  checksums **(owner:** binaries in GitHub releases), and a multi-arch (amd64, arm64)
  image in GitHub Container Registry tagged with the version **(owner:** Docker
  Compose). The workflow refuses a tag that disagrees with the version, and takes the
  notes from `docs/changelog.md`.

## 17. Two releases **(owner)**

- **Release 1 — cutover, lean. (owner, 2026-10-24)** The least that the first board
  needs, so that it moves to gobb as soon as possible. In the last year, 10 people
  wrote 54 posts and started 9 topics, attached 2 files and sent about 3 private
  messages, and the last poll was in 2019. Release 1 holds:
  - the read side (Plan 3);
  - posting: a new topic, a reply, a quote, and the edit and delete of one's own posts;
  - the profile: change the password, the email address and the signature, and pick a
    color scheme. Added by the owner on 2026-10-25:
    - the time zone;
    - the date format: the current one ("Oct 25, 2026, 5:07 pm"), relative ("just
      now", "92 seconds ago", "98 minutes ago", "39 hours ago", "96 days ago", "14
      months ago", "3 years ago"), or ISO 8601 in the member's zone with the offset
      ("2026-10-25T17:07:00-04:00"). Guests see the current one;
    - the avatar: upload, replace or remove;
    - "About you": location, occupation, interests and website, shown on the profile.
      A website is `http` or `https` only, and its link carries `rel="nofollow ugc"`;
    - the birthday: day and month, and the year if the member wants the age shown. A
      year later than the current year minus 19 is refused: the first board is not
      for children. The profile shows the age when the year is set, and the board
      index lists the birthdays of the day;
    - links to the account page and to the messages in the header;
  - private messages, added by the owner on 2026-10-25: the inbox and the sent list,
    with the imported messages; send, reply and quote, to one or more members; delete a
    message from one's own view; and an unread count in the header;
  - moderation on the page: delete or restore a post, edit a post, lock a topic, and
    approve a held post;
  - admin commands: reset a password, and ban or unban a user;
  - the release of the image on a tag, a rehearsal on the production host, and the
    cutover.

  Sign-up stays closed, as it is on the first board today.

  **On cutover day, members keep what they use (owner, 2026-10-25):** their settings
  and their message history.

  **Every imported setting is one the member can change (owner, 2026-10-25).** A
  per-user setting that changes what a member sees comes into gobb only with a page
  where the member changes it. A setting with no such page stays out of the bundle,
  and the member gets gobb's default. For the first board, the bundle leaves out
  display options, posting defaults, privacy and email switches, topic watches,
  bookmarks, notification settings, message drafts, folders and rules, and the
  memberships of groups that members join freely and that grant no rights. A later
  feature that brings one back brings its page with it.

  **Cutover is done when (owner):**
  - gobb serves the board's website;
  - the old software's process no longer runs on the host;
  - every member account still signs in with its existing password;
  - every member can read the private messages they had, and set their time zone.

  The last point is checked in three ways: every imported hash is in a format that
  gobb verifies (all of them, counted); the legacy test vectors pass; and the owner
  signs in with a real account on the rehearsal and again after cutover.
- **After cutover.** Everything else in sections 7 to 16 comes in later releases. The
  data for each part is imported at cutover and kept, so nothing is lost while it
  waits. This covers:
  - creating polls, and voting;
  - attachment uploads;
  - email, with notifications and password reset by email (until then, a member who
    forgets a password asks the admin);
  - the admin panel;
  - load protection, crawler readiness and trust bands.
- **Release 2 — reopen sign-up.** Turn on registration with every defense in section 10,
  after email has worked in production and the board has run on gobb for a while.

The order of work follows from this: schema and migrations; then permission
resolution, password verification and the renderer, all test-first; then the bundle
format and the importer; then the pages on top of imported data; then release 1 and
cutover; then release 2.

## 18. Cutover (general shape)

1. Rehearse: the private converter builds a bundle from a recent dump, gobb imports it,
   and the checks in sections 8.2 and 8.3 pass. Time one rehearsal on the production
   host itself, because it is much smaller than a development machine.
2. Before cutover day, run gobb on the production host under Docker Compose and test it
   through an SSH tunnel: base path, forwarded headers, CSP, email. The public proxy
   stays on the old board.
3. Put the old board in maintenance mode and take a final dump. Downtime budget: one
   hour. **(designer's call:** the board gets about 50 posts a year.)
4. Build the bundle, import it, and run the checks.
5. Point the reverse proxy at gobb; smoke-test reading, login, posting, an old URL.
6. Keep the old board's files and final dump for rollback for an agreed window.
7. Remove the old software from the host.

Host-specific steps live with the deployment, outside this repo.

## 19. Left to the builder

- Schema details, package layout, router, template structure, CSS approach.
- The exact new URL shapes (within section 14).
- Search ranking.
- Whether to cache anything beyond the guest page cache (section 10, "Load
  protection").
- The numbers that "Load protection" leaves open: the pool size, the request budgets,
  the search limits and the cache's memory limit. Prove them with a load test against
  the image under the memory cap. Under the test, the server must answer 429 or 503
  quickly, stay under the cap, and recover without a restart.
- Default numbers: holding-area size, rate limits, upload limits, argon2id cost. The
  production host has 1 GB of RAM; size argon2id memory for that (for example 19 MiB,
  t=2, p=1) and test the server under a memory cap.
