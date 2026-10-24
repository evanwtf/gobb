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
