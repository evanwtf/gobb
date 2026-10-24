# gobb Plan 1: Foundation — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the gobb repo with its guards and CI, the database layer and the release-1 schema, the cores every later plan depends on (legacy and gobb password checks, permission resolution, the public `markup` package), and a container image that runs under Docker Compose.

**Architecture:** One Go module, one binary (`cmd/gobb`) with subcommands. The `markup` package is public, at the module root, because converters import it. Everything else lives under `internal/`, one package per responsibility: `config`, `db` (pool, migrations, schema), `dbtest` (throwaway databases for tests), `password`, `acl`, `web`. Posts are stored as Markdown source plus a parse tree (`markup.Node`, as JSON); pages render from the tree.

**Tech Stack:** Go 1.27; PostgreSQL 16+; Docker (Chainguard images) and Docker Compose; `github.com/jackc/pgx/v5`; `golang.org/x/crypto` (bcrypt, argon2); `golang.org/x/text/unicode/norm`; `github.com/yuin/goldmark` (Markdown); tool dependencies `honnef.co/go/tools/cmd/staticcheck` and `golang.org/x/vuln/cmd/govulncheck` (via `go get -tool`).

**Spec:** `docs/superpowers/specs/2026-10-23-gobb-design.md` (sections 1, 8, 9, 10, 12, 15, 16). Roadmap: `docs/superpowers/plans/2026-10-23-gobb-roadmap.md`.

**Workers:** Sonnet, high effort, unless a task says "Worker: Haiku, max effort". Reviewer: Opus, high effort, after every task and over the whole plan at the end.

## Global Constraints

- **Clean room.** Work only from this repo and public standards. Do not read, search for or copy the source, templates, stylesheets or images of any other forum software. If a task needs a fact about another system, stop and ask. (`AGENTS.md`; spec 1.)
- Module path `github.com/evanwtf/gobb`; `go 1.27` in `go.mod`; build with the latest Go 1.27.x.
- Runtime dependencies: only pgx v5, `golang.org/x/crypto`, `golang.org/x/text`, and `github.com/yuin/goldmark` (Task 10). Anything more needs a reason in `docs/changelog.md` (spec 15).
- Add each dependency at its latest release at the time of the task (`go get <module>@latest`); `go.sum` pins it.
- This repo is public. Never commit hostnames, IP addresses other than `10.x`, home-directory paths, secret values, secret-manager item names, or any forum data. Tests use hand-written fixtures only.
- Commit subjects: `area: imperative summary` (for example `password: verify legacy chain hashes`). No agent session URLs or IDs, no `Claude-Session:` or `Co-Authored-By` agent trailers.
- Work lands on `main`; push after every task.
- Tests that need PostgreSQL read `GOBB_TEST_DATABASE_URL` and call `t.Skip` when it is unset. CI sets it and fails on any skip.
- Logs go through `log/slog` to stdout. Command results (for example `gobb version`) print to stdout.
- Times are stored as `timestamptz`; IDs are `bigint`.
- Container images build on Chainguard: `cgr.dev/chainguard/go` to build, `cgr.dev/chainguard/static` to run, user `nonroot`, `CGO_ENABLED=0`. The compose `postgres` service uses the official `postgres:16` image (spec 15 gives the reason).

## Review Focus

1. **Hostile or huge passwords** — a password over 4,096 bytes, or invalid UTF-8, must return false quickly, without running argon2 or bcrypt. Tests: `TestVerifyLegacyRejectsHugeAndInvalid` (Task 6), `TestVerifyGobbRejectsHuge` (Task 7).
2. **Deeply nested or unbalanced Markdown** — 10,000 nested `>` quotes, or a post full of unclosed `**`, `||` or `[`, must parse in near-linear time and must not exhaust the stack. Nesting beyond the caps becomes plain text. Test: `TestParseNestingCaps` (Task 10).
3. **Dangerous link schemes** — `javascript:`, `data:`, `vbscript:`, mixed-case or entity-encoded variants in `url`, `img` or `email` must never reach an `href` or `src`. Tests: `TestValidateRejects` (Tasks 9, 14), `TestParseDangerousLinks` (Task 10), `TestRenderRejectsDangerousSchemes` (Task 11).
4. **Hostile imported trees** — a bundle tree with unknown elements, bad attributes, wrong children or extreme depth must fail `Validate` with an error, never a panic or a stack overflow. Tests: `TestValidateRejects`, `TestValidateDeepTree` (Task 9).
5. **Two processes migrating at once, or an edited migration** — concurrent `Migrate` calls must apply each migration once. An applied migration whose file changed must make `Migrate` refuse. Tests: `TestMigrateConcurrent`, `TestMigrateRefusesChangedChecksum` (Task 3).

---

## Revision 1 (2026-10-23): Markdown replaces BBCode

The owner chose Markdown for posts, with no BBCode (spec 12). Spoilers use `||text||`.
A bare link to a supported site, alone on its line, becomes an embed. There is no
underline, color, size, shout or comic, and there are no tag aliases.

- Task 9 landed as package `bbcode`. **Task 14** reshapes it into package `markup`.
- Tasks 10, 11 and 12 below are rewritten for Markdown.
- **Task 15** renames the schema columns and the two permission options, and drops the
  `tag_aliases` table.
- Order: Task 15 at any time. Task 14 first; then Tasks 10 and 11 in parallel (they
  touch different files); then Task 12.
- The literal-URL contract supersedes the "decode entities" text in Tasks 9 and 14.
  A stored URL is a literal string, and nothing decodes entities in it. The renderer
  escapes it.
- The final fix wave replaced `RenderContext.SmileyURL` with `RenderContext.Smiley`,
  which returns a `Smiley` whose `Image` and `Alt` the renderer uses.

---

### Task 1: Scaffold, version, and repo guards

**Before this task:** the controller places the owner's denylist at `.public-denylist` in the working tree. Workers never open it or print it.

**Files:**
- Create: `go.mod`, `cmd/gobb/main.go`, `internal/version/version.go`, `internal/version/version_test.go`, `docs/changelog.md`
- Create: `scripts/check-public.sh`, `scripts/refuse-session-ids.sh`, `scripts/guards_test.go`
- Create: `.pre-commit-config.yaml`
- Modify: `.gitignore` (add `.public-denylist`, `/gobb`, `/dist/`), `README.md` (add Bootstrap), `AGENTS.md` (add Commands)

**Interfaces:**
- Produces: `version.Version` (`const Version = "0.1.0"`); `gobb version` prints `gobb 0.1.0` and exits 0; an unknown subcommand prints usage to stderr and exits 2. `main` dispatches on `os.Args[1]` with a `map[string]func(ctx context.Context, args []string) error`; later tasks add `migrate`, `serve` and `healthcheck`, later plans `import` and `perms`.
- Produces: `scripts/check-public.sh [files...]` — with no arguments it checks `git ls-files`; exits 1 and prints `file:line` (never the matched text) on a hit. It reads the denylist from `$DENYLIST_FILE` if set, else `.public-denylist` if present.
- Produces: `scripts/refuse-session-ids.sh <commit-msg-file>` — exits 1 on a hit.

- [ ] **Step 1: Write the failing tests**

`internal/version/version_test.go`:
- `TestVersionMatchesChangelog` — reads `../../docs/changelog.md`, finds the first line that starts with `## `, takes the first field after `## `, and asserts it equals `Version`.

`scripts/guards_test.go` (package `scripts`; each case writes files into `t.TempDir()`, runs `git init` there, adds the files, and runs the script with `exec.Command("sh", ...)` from that directory). Build the private-looking inputs by string concatenation, so no complete one appears in the repo:
- `TestCheckPublicFlagsLANAddress` — a file `notes.txt` containing one address from `192.168/16` → exit 1, output contains `notes.txt:1`, output does not contain the address.
- `TestCheckPublicFlags172Private` — one address from `172.16/12` → exit 1.
- `TestCheckPublicAllows10Net` — one address from `10/8` → exit 0.
- `TestCheckPublicFlagsHomePath` — a macOS home path and a Linux home path, each with a made-up user name → exit 1 each.
- `TestCheckPublicFlagsPrivateDNS` — one host name with each private suffix in the pattern list below → exit 1 each.
- `TestCheckPublicUsesDenylist` — a denylist file containing `examplecorp`, passed with `DENYLIST_FILE`; a file containing `ExampleCorp` → exit 1 (case-insensitive, fixed string), and the output does not contain the word. The same check through a `.public-denylist` in the temp repo → exit 1, and that file itself is never scanned.
- `TestCheckPublicCleanRepo` — only ordinary text → exit 0.
- `TestRefuseSessionIDs` — exit 1 for each of: an agent session URL with a made-up 24-character ID, a `Claude-Session:` trailer, a `Co-Authored-By:` trailer naming Claude, and a bare `session_` token followed by 24 made-up characters. Exit 0 for `fix session_store` and for a `Co-Authored-By:` trailer naming a person.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/version/ ./scripts/`
Expected: FAIL — `undefined: Version`; the scripts do not exist.

- [ ] **Step 3: Implement**

- `go mod init github.com/evanwtf/gobb`, then set `go 1.27`.
- `version.go`: `const Version = "0.1.0"`.
- `docs/changelog.md`: `# Changelog` then `## 0.1.0 (unreleased)` with one bullet per task as tasks land.
- `cmd/gobb/main.go`: `slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))`, dispatch as in Interfaces.
- `check-public.sh`: POSIX `sh`, `grep -E`/`grep -F` only (it must run from a bare `git commit`). Patterns: `192\.168\.[0-9]+\.[0-9]+`, `172\.(1[6-9]|2[0-9]|3[01])\.[0-9]+\.[0-9]+`, `/Users/[A-Za-z0-9._-]+/`, `/home/[A-Za-z0-9._-]+/`, `\.(internal|lan|home\.arpa|localdomain)\b`. Denylist: one fixed string per line, blank lines and `#` comments ignored, matched with `grep -i -F`. Skip the script itself, `scripts/guards_test.go`, and `.public-denylist`. Skip binary files.
- `refuse-session-ids.sh`: fail on `claude\.ai/code/session`, `Claude-Session:`, `Co-Authored-By:.*Claude`, `session_[A-Za-z0-9]{20,}`.
- `.pre-commit-config.yaml`, in this order:
  1. local `check-public` (`entry: scripts/check-public.sh`, `language: script`, `pass_filenames: true`) first, as the data guard;
  2. `pre-commit-hooks`: `check-added-large-files`, `check-merge-conflict`, `check-yaml`, `end-of-file-fixer`, `trailing-whitespace`, `detect-private-key`;
  3. `gitleaks`;
  4. local `gofmt` (`entry: gofmt -l -w`, `language: system`, `types: [go]`);
  5. local `go-vet` (`entry: go vet ./...`, `language: system`, `pass_filenames: false`);
  6. local `refuse-session-ids` (`stages: [commit-msg]`, `language: script`).

  Use each hook repo's latest release tag.
- `README.md` Bootstrap, first section after the title, commands verified by running them:

  ```sh
  brew install go            # or https://go.dev/dl/ — Go 1.27
  git clone https://github.com/evanwtf/gobb && cd gobb
  go build ./cmd/gobb && ./gobb version
  go test ./...
  uvx pre-commit install --hook-type pre-commit --hook-type commit-msg
  ```

- `AGENTS.md` gets a `## Commands` section with the same build, test and hook commands, plus the `GOBB_TEST_DATABASE_URL` note.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/version/ ./scripts/ && go build ./cmd/gobb && ./gobb version`
Expected: PASS; prints `gobb 0.1.0`.
Run: `sh scripts/check-public.sh && uvx pre-commit install --hook-type pre-commit --hook-type commit-msg && uvx pre-commit run --all-files`
Expected: the guard exits 0 on the repo with the real denylist; every hook passes.

- [ ] **Step 5: Commit and push**

```bash
git add -A && git commit -m "repo: scaffold, version, and public-repo guards" && git push
```

---

### Task 2: CI workflow

**Before this task:** the controller sets the repository secret from the local denylist: `gh secret set PUBLIC_DENYLIST < .public-denylist`.

**Files:**
- Create: `.github/workflows/ci.yml`
- Modify: `go.mod`/`go.sum` (tool dependencies)

**Interfaces:**
- Consumes: `scripts/check-public.sh` (Task 1).
- Produces: a `check` job required by every later task; the env var `GOBB_TEST_DATABASE_URL` in CI.

- [ ] **Step 1: Add the tool dependencies**

Run: `go get -tool honnef.co/go/tools/cmd/staticcheck@latest && go get -tool golang.org/x/vuln/cmd/govulncheck@latest`
Expected: both appear in a `tool` block in `go.mod`; `go tool staticcheck ./...` and `go tool govulncheck ./...` exit 0.

- [ ] **Step 2: Write `.github/workflows/ci.yml`**

- `on: push: branches: [main]` and `pull_request`.
- Job `fork-guard`: `runs-on: ubuntu-latest`, runs only on a pull request from a fork (`github.event.pull_request.head.repo.full_name != github.repository`), and exits 1 with `A maintainer must push this branch to the repo to run the tests.`
- Job `check`: `runs-on: [self-hosted, Linux, X64]`, `timeout-minutes: 15`, `if: github.event_name != 'pull_request' || github.event.pull_request.head.repo.full_name == github.repository`.
- Service `postgres`: image `postgres:16`, env `POSTGRES_PASSWORD: gobb`, `ports: ["5432"]` (random host port; the runner is shared), health check `pg_isready`.
- Steps:
  1. `actions/checkout` (current major);
  2. a step that writes `GOCACHE=$RUNNER_TEMP/gocache-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT` and `GOMODCACHE=$RUNNER_TEMP/gomod-$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT` to `$GITHUB_ENV` (shared-runner cache scoping);
  3. `actions/setup-go` (current major) with `go-version-file: go.mod`, `cache: false`;
  4. the public guard: with `env: PUBLIC_DENYLIST: ${{ secrets.PUBLIC_DENYLIST }}`, fail with `PUBLIC_DENYLIST secret is empty` if it is empty, write it to `$RUNNER_TEMP/denylist` (mode 600), then run `DENYLIST_FILE=$RUNNER_TEMP/denylist sh scripts/check-public.sh`. Never echo the variable;
  5. one step collecting status across `test -z "$(gofmt -l .)"`, `go vet ./...`, `go tool staticcheck ./...`, `go tool govulncheck ./...` (run all, then exit with the first failure's status);
  6. tests with `GOBB_TEST_DATABASE_URL=postgres://postgres:gobb@localhost:${{ job.services.postgres.ports['5432'] }}/postgres?sslmode=disable`: `go test -race -json ./... > test.json; status=$?`, print the failures, then fail if `grep -q '"Action":"skip"' test.json` (message `unexpected skipped test`), else exit with `$status`.

- [ ] **Step 3: Verify on GitHub**

Run: `git add -A && git commit -m "ci: check job with Postgres, guards, and fork guard" && git push && gh run watch --exit-status $(gh run list --workflow ci.yml --limit 1 --json databaseId -q '.[0].databaseId')`
Expected: the `check` job passes; `fork-guard` is skipped on a push.

---

### Task 3: Config, database pool, migrations, test databases

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`
- Create: `internal/db/db.go`, `internal/db/migrate.go`, `internal/db/migrate_test.go`, `internal/db/migrations/0000_bootstrap.sql`
- Create: `internal/dbtest/dbtest.go`
- Modify: `cmd/gobb/main.go` (add `migrate`)

**Interfaces:**
- Produces:
  - `config.Config{DSN, Listen, DataDir, BasePath string; TrustedProxies []netip.Prefix}`; `config.FromEnv(getenv func(string) string) (config.Config, error)`.
    - `GOBB_DSN` is required.
    - Defaults: `GOBB_LISTEN=127.0.0.1:8080`, `GOBB_DATA_DIR=/var/lib/gobb`, `GOBB_BASE_PATH=/`, `GOBB_TRUSTED_PROXIES=127.0.0.1/32,::1/128` (comma-separated CIDRs).
    - `BasePath` always starts and ends with `/`.
  - `db.Open(ctx context.Context, dsn string) (*pgxpool.Pool, error)` — pings before returning.
  - `db.Querier` — `interface { Exec(ctx, sql, args...) (pgconn.CommandTag, error); Query(...) (pgx.Rows, error); QueryRow(...) pgx.Row }`, satisfied by `*pgxpool.Pool` and `pgx.Tx`.
  - `db.Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error)` — the embedded migrations; returns the file names it applied.
  - `db.MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) ([]string, error)` — the same over any FS of `NNNN_name.sql` files.
  - `dbtest.New(t testing.TB) *pgxpool.Pool` — skips when `GOBB_TEST_DATABASE_URL` is unset; creates `gobb_test_<16 hex>`, runs `db.Migrate`, drops it in `t.Cleanup`.
  - `gobb migrate` — loads config, migrates, logs each applied file.

- [ ] **Step 1: Write the failing tests**

- `TestFromEnvRequiresDSN` — empty env → error mentioning `GOBB_DSN`.
- `TestFromEnvDefaults` — only `GOBB_DSN` set → `Listen == "127.0.0.1:8080"`, `DataDir == "/var/lib/gobb"`, `BasePath == "/"`.
- `TestFromEnvNormalizesBasePath` — `forums` → `/forums/`; `/forums` → `/forums/`.
- `TestFromEnvTrustedProxies` — the default gives `127.0.0.1/32` and `::1/128`; `10.231.0.1/32, 10.0.0.0/8` parses to two prefixes; `not-a-cidr` → error naming `GOBB_TRUSTED_PROXIES`.
- `TestMigrateAppliesInOrder` — MapFS with `0002_b.sql` and `0001_a.sql` → returns `["0001_a.sql","0002_b.sql"]`; both tables exist.
- `TestMigrateIdempotent` — a second run returns an empty slice.
- `TestMigrateRefusesChangedChecksum` — apply `0001_a.sql`, then run again with different content under the same name → error containing `0001_a.sql changed after it was applied`.
- `TestMigrateConcurrent` — two goroutines call `MigrateFS` on the same database at once → both succeed; `schema_migrations` has one row per file.
- `TestMigrateRejectsBadName` — a file `a.sql` → error.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/config/ ./internal/db/`
Expected: FAIL (undefined symbols).

- [ ] **Step 3: Implement**

Migrations:
- Table `schema_migrations(version text primary key, checksum text not null, applied_at timestamptz not null default now())`, created by `MigrateFS` itself if missing.
- Take `pg_advisory_lock(7212026)` on one dedicated connection for the whole run, and release it in a defer.
- Apply each pending file in its own transaction. The checksum is the hex SHA-256 of the file bytes.
- `0000_bootstrap.sql` holds only `select 1;`, so the embedded set is never empty.

- [ ] **Step 4: Run them to verify they pass**

Run: `GOBB_TEST_DATABASE_URL=postgres://... go test -race ./internal/config/ ./internal/db/`
Expected: PASS, with no skips.

- [ ] **Step 5: Commit and push**

```bash
git add -A && git commit -m "db: config, pool, and locked checksummed migrations" && git push
```

---

### Task 4: The release-1 schema

**Files:**
- Create: `internal/db/migrations/0001_schema.sql`, `internal/db/sequences.go`, `internal/db/schema_test.go`

**Interfaces:**
- Consumes: `db.Migrate`, `dbtest.New` (Task 3).
- Produces: the tables below (later plans depend on these names), and `db.ResetSequences(ctx context.Context, q db.Querier) error`. It sets every identity column's sequence past `max(id)`, so the importer can insert bundle IDs explicitly.

Every `id` is `bigint generated by default as identity primary key` unless noted. Enumerations are `text` with a `check` constraint. Foreign keys are on, with `on delete cascade` only where noted.

| Table | Columns (beyond `id`) |
|---|---|
| `users` | `username text not null`, `username_clean text not null unique`, `email text not null`, `email_clean text not null` (indexed, not unique — 2 shared addresses exist), `password_hash text not null`, `password_scheme text not null check in ('legacy','gobb')`, `state text not null check in ('active','inactive')`, `is_owner bool not null default false`, `email_verified bool not null`, `registered_at`, `last_visit_at`, `last_mark_at timestamptz` (the member's "mark all read" time), `post_count int not null default 0`, `rank_id bigint null`, `avatar_path text null`, `signature_source text not null default ''`, `signature_tree jsonb null`, `timezone text not null default 'America/New_York'`, `color_scheme text not null default 'slate' check in ('slate','slate-deep','slate-light')`, `notify_on_post bool not null default true` *Note: the final fix wave added `signature_markdown`, `signature_smilies` and `signature_autolink`, each `bool not null default true`.* |
| `gone_users` | `id bigint primary key` (IDs not imported; their profiles answer 410) |
| `groups` | `name text not null`, `builtin text null unique check in ('guests','members','moderators','admins')`, `kind text not null check in ('open','request','closed','hidden','builtin')`, `description text not null default ''` |
| `group_members` | `group_id`, `user_id` (both cascade), `is_leader bool`, `is_pending bool`; pk `(group_id, user_id)` |
| `acl_roles` | `name text not null`, `kind text check in ('forum','mod','board')`, `description text`, `position int` |
| `acl_role_settings` | `role_id` (cascade), `option text`, `setting smallint check in (1,2,3)` (No, Never, Yes as in `acl.Setting`); pk `(role_id, option)` |
| `acl_group_grants` | `group_id` (cascade), `forum_id bigint not null default 0`, `role_id bigint null`, `option text null`, `setting smallint null`; check: exactly one of `role_id` and (`option`,`setting`) |
| `acl_user_grants` | as `acl_group_grants`, with `user_id` |
| `forums` | `parent_id bigint null`, `kind text check in ('category','forum','link')`, `name`, `description`, `rules text`, `link_url text null`, `locked bool`, `position int not null`, `topic_count int`, `post_count int`, `last_post_id bigint null` |
| `topics` | `forum_id`, `title`, `author_id bigint null`, `author_name text`, `created_at`, `last_post_id bigint null`, `last_post_at`, `reply_count int`, `view_count int`, `kind text check in ('normal','sticky','announce','global')`, `locked bool`, `visibility text check in ('approved','unapproved','deleted')`, `moved_to_topic_id bigint null` |
| `posts` | `topic_id`, `forum_id`, `author_id bigint null`, `author_name text`, `author_ip inet null`, `created_at`, `subject text`, `body_source text not null`, `body_tree jsonb not null`, `body_text text not null` (plain text for search), `enable_bbcode`, `enable_smilies`, `enable_magic_url`, `enable_sig bool not null`, `visibility text check in ('approved','unapproved','deleted')`, `edit_count int`, `edited_at timestamptz null`, `edited_by bigint null`, `edit_reason text not null default ''`, `search tsvector generated always as (to_tsvector('english', subject || ' ' || body_text)) stored` with a GIN index; index `(topic_id, created_at)` *Note: `enable_bbcode` and `enable_magic_url` became `enable_markdown` and `enable_autolink` (Task 15). The three markup flags now default to true.* |
| `polls` | `topic_id bigint primary key` (cascade), `question`, `max_options int`, `ends_at timestamptz null`, `can_change_vote bool` |
| `poll_options` | `topic_id` (cascade), `option_id int`, `text`, `vote_count int`; pk `(topic_id, option_id)` |
| `poll_votes` | `topic_id`, `option_id`, `user_id`, `voter_ip inet null`; pk `(topic_id, option_id, user_id)` |
| `attachments` | `post_id bigint null`, `position int not null`, `uploader_id`, `real_name text`, `stored_path text`, `mime text`, `size bigint`, `sha256 text`, `width int null`, `height int null`, `download_count int`, `comment text`, `created_at` |
| `pms` | `author_id`, `author_ip inet null`, `created_at`, `subject`, `body_source`, `body_tree jsonb`, `body_text`, `enable_bbcode`, `enable_smilies`, `enable_magic_url`, `enable_sig bool`, `author_deleted bool not null default false` *Note: `enable_bbcode` and `enable_magic_url` became `enable_markdown` and `enable_autolink` (Task 15). The three markup flags now default to true.* |
| `pm_recipients` | `pm_id` (cascade), `user_id`, `kind text check in ('to','bcc')`, `is_read bool`, `is_deleted bool`; pk `(pm_id, user_id)` |
| `topic_watches` | `user_id`, `topic_id` (cascade), `notify_pending bool`; pk `(user_id, topic_id)` |
| `bookmarks` | `user_id`, `topic_id`; pk both |
| `topic_marks` / `forum_marks` | `user_id`, `topic_id` / `forum_id`, `marked_at timestamptz`; pk the pair |
| `bans` | `kind text check in ('user','email','ip')`, `user_id bigint null`, `email text null`, `ip cidr null`, `reason text`, `reason_shown text`, `created_at`, `ends_at timestamptz null`, `created_by bigint null`, `undo_log_id bigint null` |
| `ranks` | `title`, `min_posts int`, `special bool`, `image_path text null` |
| `smilies` | `code text unique`, `image_path`, `description`, `position int` |
| `tag_aliases` | `name text primary key`, `tag text not null check in ('b','i','u','shout','comic','spoiler')`, `label text null` (only for `spoiler`) *Note: Task 15 dropped this table.* |
| `legacy_routes` | `path text not null` (old path under the base path, no leading slash), `match jsonb not null default '{}'` (query parameters that must have these values), `id_param text null` (query parameter that holds the target ID), `target_kind text not null check in ('home','forum','topic','post','user','members','team','attachment','avatar','search','account','page')`, `target_id bigint null` (fixed target when `id_param` is null), `target_slug text null` (for `page`), `offset_param text null` (item offset that picks the page), `position int not null` |
| `legacy_files` | `prefix text primary key` (old path prefix under the base path), `dir text not null check in ('smilies','ranks')` |
| `sessions` | `id text primary key` (SHA-256 hex of the token), `user_id` (cascade), `created_at`, `last_seen_at`, `expires_at`, `ip inet`, `user_agent text`, `remember bool` |
| `user_ips` | `user_id`, `ip inet`, `last_seen_at`; pk `(user_id, ip)` |
| `settings` | `key text primary key`, `value jsonb not null` |
| `mod_log` | `actor_id bigint null`, `action text`, `target_type text`, `target_id bigint`, `details jsonb`, `created_at`, `undo_until timestamptz null`, `undone_at timestamptz null` |
| `email_queue` | `to_addr`, `subject`, `body`, `created_at`, `attempts int`, `next_attempt_at`, `sent_at timestamptz null`, `last_error text` |
| `email_tokens` | `token_hash text primary key`, `user_id` (cascade), `purpose text check in ('verify','reset')`, `expires_at`, `used_at timestamptz null` |
| `static_pages` | `slug text primary key`, `title`, `body_source`, `body_tree jsonb`, `updated_at` |

- [ ] **Step 1: Write the failing tests**

- `TestSchemaTables` — after `dbtest.New`, every table name above exists in `information_schema.tables`.
- `TestExplicitIDsThenResetSequences` — insert a user with `id = 54`; `ResetSequences`; insert a user with no `id` → its `id` is 55.
- `TestGrantExactlyOneOf` — an `acl_group_grants` row with both `role_id` and `option` set fails; one with neither fails.
- `TestPostSearchVector` — insert a post with `body_text` "server rebuilt"; `select ... where search @@ plainto_tsquery('english','rebuild')` finds it.
- `TestTagAliasTarget` — an alias to `shout` inserts; an alias to `script` fails.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/db/ -run 'Schema|Explicit|Grant|Search|Alias'`
Expected: FAIL (tables missing).

- [ ] **Step 3: Implement** `0001_schema.sql` and `db.ResetSequences`. For each table with an identity `id`, query `pg_get_serial_sequence`, then `setval(seq, coalesce(max(id),0)+1, false)`.

- [ ] **Step 4: Run them to verify they pass**

Run: `GOBB_TEST_DATABASE_URL=... go test -race ./internal/db/`
Expected: PASS.

- [ ] **Step 5: Commit and push**

```bash
git add -A && git commit -m "db: release-1 schema and sequence reset" && git push
```

---

### Task 5: The legacy password form

**Worker: Haiku, max effort.**

**Files:**
- Create: `internal/password/legacyform.go`, `internal/password/legacyform_test.go`

**Interfaces:**
- Produces: `password.LegacyForm(raw string) (string, bool)` — spec 10, steps 1 to 4; `false` when `raw` is not valid UTF-8.

- [ ] **Step 1: Write the failing test** `TestLegacyForm`, a table of `raw → want, ok`:

| raw | want | ok |
|---|---|---|
| `a&b` | `a&amp;b` | true |
| `say "hi"` | `say &quot;hi&quot;` | true |
| `<x>` | `&lt;x&gt;` | true |
| `it's` | `it's` | true |
| `&amp;` | `&amp;amp;` | true |
| `  pw  ` | `pw` | true |
| `"\x0Bpw\t"` | `pw` | true |
| `"a\r\nb"` | `"a\nb"` | true |
| `"a\rb"` | `"a\nb"` | true |
| `"a\x00b"` | `ab` | true |
| `"﻿pw"` | `pw` | true |
| `"café"` | `"café"` | true |
| `"\xff"` | `""` | false |

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/password/ -run LegacyForm`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement** with `utf8.ValidString`, `strings.NewReplacer`, `norm.NFC.String`, and `strings.Trim(s, " \t\n\r\x00\x0B")`, in the spec's order.

- [ ] **Step 4: Run it to verify it passes**

Run: `go test ./internal/password/ -run LegacyForm`
Expected: PASS.

- [ ] **Step 5: Commit and push**

```bash
git add -A && git commit -m "password: legacy password form" && git push
```

---

### Task 6: Verify legacy hashes

**Before this task:** the owner's side supplies `internal/password/testdata/legacy-vectors.json` and `internal/password/testdata/README.md`. If they are missing, stop and report; do not make vectors yourself.

**Files:**
- Create: `internal/password/legacy.go`, `internal/password/phpass.go`, `internal/password/legacy_test.go`, `internal/password/phpass_test.go`

**Interfaces:**
- Consumes: `LegacyForm` (Task 5).
- Produces: `password.VerifyLegacy(raw, stored string) bool`, and the internal helpers `phpassDigest(setting, input string) (string, bool)` and `parsePHC(s string) (phc, error)` that Task 7 reuses.

The vectors file is a JSON array of `{"name", "raw_b64", "hash", "want"}`. `raw_b64` is the raw password bytes in standard base64. `want` is the old software's own verdict. The file covers four formats (bcrypt, argon2id, chain, and chain over the MD5 of the input) for 15 made-up passwords. Each vector has a negative twin (the same hash with a different password, `want: false`): 120 entries.

- [ ] **Step 1: Write the failing tests**

- `TestPhpassKnownAnswer` — `phpassDigest("9IQRaTwmf", "test12345")` returns `"eRo7ud9Fh4E2PdI0S3r.L0", true`. This is the portable hash `$P$9IQRaTwmfeRo7ud9Fh4E2PdI0S3r.L0` from the public-domain phpass distribution's own test file.
- `TestPhpassBadSetting` — a setting of the wrong length, or a count character whose alphabet index is outside 7 to 30 → `"", false`.
- `TestVerifyLegacyVectors` — for every vector, `VerifyLegacy(base64decode(raw_b64), hash) == want`.
- `TestVerifyLegacyEmptyHash` — `VerifyLegacy("anything", "") == false`.
- `TestVerifyLegacyUnknownFormat` — `$P$...`, `$chain$md5,...` and `plain` → false.
- `TestVerifyLegacyRejectsHugeAndInvalid` — 4,097 bytes of `a` against a bcrypt vector → false in under 5 ms; `"\xff"` → false.

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/password/ -run 'Phpass|VerifyLegacy'`
Expected: FAIL (undefined).

- [ ] **Step 3: Implement**

Flow: `LegacyForm`; if not ok, or the form is over 4,096 bytes, return false. Then dispatch on the prefix:

- `$2y$`, `$2a$`, `$2b$` → `bcrypt.CompareHashAndPassword`. If the library rejects `$2y$`, substitute `$2a$`: the algorithm is the same for these inputs.
- `$argon2id$` → parse the PHC string `$argon2id$v=19$m=<KiB>,t=<n>,p=<n>$<salt>$<hash>` (base64 `RawStdEncoding`). Compute `argon2.IDKey` with those parameters and the hash length, and compare with `subtle.ConstantTimeCompare`. Refuse `m` over 262,144 KiB, `t` over 10 or `p` over 8, so a stored hash cannot make a login use unbounded memory or time.
- `$chain$phpass,` → the setting is the next 9 characters; the rest of the string is a bcrypt hash and must start with `$2`. For `in` in (the form, the lowercase hex MD5 of the form): `d, ok := phpassDigest(setting, in)`; if ok and the bcrypt hash matches `d`, return true. Otherwise return false.
- Anything else → false.

`phpassDigest`, from the phpass portable-hash specification:
- alphabet `./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz`;
- `count = 1 << index(setting[0])`, valid for an index of 7 to 30;
- `salt = setting[1:9]`;
- `h = md5(salt + in)`, then `count` times `h = md5(h + in)`;
- the digest is the 16 bytes of `h` in phpass's encoding: each 3-byte group, little-endian, becomes 4 alphabet characters, 6 bits at a time from the low end; the last group has 1 byte, which gives 2 characters. Total 22 characters.

The vectors are the judge.

- [ ] **Step 4: Run them to verify they pass**

Run: `go test -race ./internal/password/`
Expected: PASS, all 120 vectors.

- [ ] **Step 5: Commit and push**

```bash
git add -A && git commit -m "password: verify legacy bcrypt, argon2id and chain hashes" && git push
```

---

### Task 7: gobb's own password hashes

**Worker: Haiku, max effort.**

**Files:**
- Create: `internal/password/gobb.go`, `internal/password/gobb_test.go`

**Interfaces:**
- Consumes: `VerifyLegacy`, `parsePHC` (Task 6).
- Produces:
  - `password.Hash(raw string) (string, error)` — NFC only (no escaping), argon2id `m=19456, t=2, p=1`, 16-byte random salt, 32-byte key, PHC string `$argon2id$v=19$m=19456,t=2,p=1$<salt>$<key>`. A password over 4,096 bytes is an error.
  - `password.Verify(raw, stored, scheme string) (ok, needsRehash bool)`:
    - `scheme` is `"legacy"` or `"gobb"`; an unknown scheme gives `false, false`.
    - `needsRehash` is true when `ok` and either the scheme is `"legacy"` or the stored parameters differ from the current ones.

- [ ] **Step 1: Write the failing tests**

- `TestHashRoundTrip` — `Verify("s3cret", Hash("s3cret"), "gobb") == (true, false)`.
- `TestHashWrongPassword` — `(false, false)`.
- `TestHashNFCEquivalence` — a hash of `"café"` verifies `"café"`.
- `TestHashFormat` — matches `^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`.
- `TestVerifyLegacySchemeNeedsRehash` — a positive bcrypt vector from Task 6 with scheme `legacy` → `(true, true)`.
- `TestVerifyOldParamsNeedRehash` — a gobb hash made with `t=1` → `(true, true)`.
- `TestVerifyGobbRejectsHuge` — 4,097 bytes → `(false, false)` in under 5 ms.

- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/password/ -run 'Hash|Verify'`. Expected: FAIL.
- [ ] **Step 3: Implement**, reusing `parsePHC` from Task 6.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./internal/password/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "password: gobb argon2id hashes and rehash signal" && git push`

---

### Task 8: Permission resolution

**Files:**
- Create: `internal/acl/acl.go`, `internal/acl/options.go`, `internal/acl/acl_test.go`

**Interfaces:**
- Produces:
  - `type Setting uint8` with `Unset Setting = 0`, `No = 1`, `Never = 2`, `Yes = 3`. The database stores `No`, `Never` and `Yes` as 1, 2, 3; the zero value is never NEVER.
  - `type Scope uint8` with `Forum = 1`, `Board = 2`, `Moderator = 3`.
  - `type Option struct { Name string; Scope Scope }`; `var Options []Option`; `func Lookup(name string) (Option, bool)`.
  - `type Grant struct { ForumID int64; Option string; Setting Setting }` — `ForumID` 0 means board-wide.
  - `type Subject struct { Owner bool; Grants []Grant }` — the user's own grants plus every non-pending group's grants, with roles already expanded. Plans 2 and 3 build it.
  - `func Resolve(s Subject) Set`; `func (s Set) Has(option string, forumID int64) bool`; `func (s Set) IsAdmin() bool`.

The registry is the table in spec 9.1: 23 `Forum` options, 6 `Moderator` options (`mod.edit`, `mod.delete`, `mod.approve`, `mod.lock`, `mod.move`, `mod.view_ip`), and 19 `Board` options, `admin` among them.

Rules:
- For each `(forum, option)`, any `Never` gives false; otherwise any `Yes` gives true; otherwise false.
- `Has` uses the forum result for `Forum`, the board-wide result for `Board`, and board-wide OR forum for `Moderator`.
- An owner gets `admin` even over a `Never`.
- `Has` with an option not in the registry panics; it is a programming error.

- [ ] **Step 1: Write the failing tests**
  - `TestNeverBeatsYes` — `{2,forum.read,Yes}` + `{2,forum.read,Never}` → `Has("forum.read",2)` false.
  - `TestYesWithoutNever` — true.
  - `TestUnsetAndNoAreFalse` — false.
  - `TestForumScopeIgnoresOtherForum` — `{2,forum.read,Yes}` → `Has("forum.read",3)` false.
  - `TestModeratorScopeBoardOrForum` — `{0,mod.delete,Yes}` → `Has("mod.delete",7)` true; `{7,mod.delete,Yes}` only → `Has("mod.delete",7)` true and `Has("mod.delete",8)` false.
  - `TestBoardScopeIgnoresForumGrant` — `{5,pm.send,Yes}` → `Has("pm.send",0)` false.
  - `TestOwnerIsAdminOverNever` — `{Owner:true, Grants:{0,admin,Never}}` → `IsAdmin()` true.
  - `TestNonOwnerAdmin` — `{0,admin,Yes}` → true; with an added `Never` → false.
  - `TestHasUnknownOptionPanics`.
  - `TestRegistry` — 48 options, no duplicate names, and every name has a valid scope.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/acl/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `Set` holds `map[int64]map[string]bool`, built once in `Resolve`.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./internal/acl/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "acl: permission registry and NEVER-wins resolution" && git push`

---

### Task 9: The tree, its schema, and embed IDs

Revision 1: Task 14 reshapes this package for Markdown.

**Files:**
- Create: `bbcode/doc.go`, `bbcode/node.go`, `bbcode/validate.go`, `bbcode/embedid.go`, `bbcode/node_test.go`, `bbcode/validate_test.go`, `bbcode/embedid_test.go`

**Interfaces:**
- Produces (package `github.com/evanwtf/gobb/bbcode`, public):
  - `type Kind uint8` with `Root`, `Text`, `Element`.
  - `type Node struct { Kind Kind; Name string; Attrs map[string]string; Text string; Children []*Node }` with JSON tags `k,n,a,t,c` and `omitempty`.
  - `func Validate(root *Node) error` — checks a whole tree against the schema below. It never panics, and it walks the tree with an explicit stack.
  - `func EmbedID(tag, value string) (string, bool)` — the one rule for embed IDs, used by `Parse` and `Validate`.
  - `const MaxDepth = 32`, `const MaxQuoteDepth = 10`.

The schema. Every attribute not listed is an error. A text node has no name, attributes or children. The root appears only at the top.

| Element | Attributes | Children |
|---|---|---|
| `b`, `i`, `u`, `shout`, `comic` | none | any |
| `quote` | `author` (1–255 chars), `post`, `time`, `user` (decimal digits, up to 19) — all optional | any |
| `code` | none | text only |
| `list` | `type` optional, one of `1 a A i I disc circle square` | `li`, or text that is only whitespace |
| `li` | none | any; only inside `list` |
| `url` | `href` required (scheme `http` or `https`), `display` optional | any except `url` and `email` |
| `email` | `address` required (one `@`, no spaces, no `:`) | any except `url` and `email` |
| `img` | `src` required (scheme `http` or `https`) | none |
| `color` | `value` required: `#rgb`, `#rrggbb`, or `[a-z]{3,20}` | any |
| `size` | `value` required: an integer 50–200 | any |
| `attachment` | `index` required (digits), `name` optional | none |
| `smiley` | `code` required (1–32 chars) | none |
| `spoiler` | `label` optional (1–100 chars) | any |
| `youtube`, `vimeo`, `tweet`, `instagram` | `id` required, and `EmbedID(name, id)` returns it unchanged | none |

Scheme checks lower-case the value and decode HTML entities before they compare. Element nesting deeper than `MaxDepth`, or `quote` nesting deeper than `MaxQuoteDepth`, is an error.

`EmbedID` accepts a bare ID or a URL:

| Tag | Bare ID | URL forms |
|---|---|---|
| `youtube` | `^[A-Za-z0-9_-]{11}$` | `youtube.com/watch?v=<id>`, `youtu.be/<id>`, `youtube.com/embed/<id>`, `youtube.com/shorts/<id>` |
| `vimeo` | `^[0-9]{1,12}$` | `vimeo.com/<id>` |
| `tweet` | `^[0-9]{1,20}$` | `twitter.com/<user>/status/<id>`, `x.com/<user>/status/<id>` |
| `instagram` | `^[A-Za-z0-9_-]{1,64}$` | `instagram.com/p/<id>` |

URL forms accept `http` or `https`, an optional `www.`, and trailing query strings (ignored, except `v` for YouTube).

- [ ] **Step 1: Write the failing tests**
  - `TestTreeJSONRoundTrip` — marshal and unmarshal a tree with every element kind → equal (`reflect.DeepEqual`).
  - `TestValidateAccepts` — one valid tree per row of the schema.
  - `TestValidateRejects` — one tree per rule, each → an error that names the element:
    - unknown element `script`; unknown attribute on `b`;
    - `url` with `href` `javascript:alert(1)`, `JaVaScRiPt:x`, `data:text/html,x`, `vbscript:x`, `&#106;avascript:x`;
    - `img` with `src` `data:image/png;base64,x`;
    - `size` 300; `color` `red;background:url(x)`;
    - `li` outside `list`; `code` with an element child; `smiley` with a child; `youtube` with `id` `not an id`;
    - a text node with children; a second root inside the tree; a nil child.
  - `TestValidateDeepTree` — 33 nested `b` → error; 11 nested `quote` → error; a chain of 100,000 nested `b` built in code → an error in under 100 ms, and no panic.
  - `TestEmbedID` — for each tag, one bare ID, each accepted URL form, and one invalid value (→ `false`).
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./bbcode/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `doc.go` states the package's purpose and that the schema is the bundle contract (spec 12.1).
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./bbcode/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "bbcode: tree type, schema validation, and embed IDs" && git push`

---

### Task 10: Markdown parser

Revision 1: this replaces the BBCode parser. It needs Task 14.

**Files:**
- Create: `markup/parse.go`, `markup/spoiler.go`, `markup/parse_test.go`
- Modify: `go.mod`, `go.sum` (add `github.com/yuin/goldmark` at its latest release), `docs/changelog.md` (the reason for the dependency: spec 12.1)

**Interfaces:**
- Consumes: `Node`, `Validate`, `EmbedID`, `CheckLinkURL`, `CheckImageURL`, `MaxDepth`, `MaxQuoteDepth` (Task 14).
- Produces:
  - `type Smiley struct { Code, Image, Alt string }`.
  - `type Options struct { Markdown, Smilies, Autolink bool; SmileyList []Smiley }`.
  - `func Parse(src string, opt Options) *Node`. It never fails, and its output always passes `Validate`.

Rules:

- **Input.** Replace invalid UTF-8 with U+FFFD first (`strings.ToValidUTF8`).
- **Parsing.** goldmark parses CommonMark. Its Linkify extension is on when `opt.Autolink` is on. Map goldmark's tree to `Node`. goldmark renders nothing.
- **Line breaks.** A soft line break becomes `br`, the same as a hard break.
- **Spoilers.** `||…||` is a spoiler. It is an inline delimiter extension in this package and pairs like emphasis. An unpaired `||` stays text. A spoiler cannot hold a spoiler.
- **Raw HTML**, inline or block, becomes text with its exact source.
- **Bad destinations.**
  - `[text](url)` whose destination fails `CheckLinkURL` becomes its text children, with no link.
  - `![alt](src)` whose source fails `CheckImageURL` becomes its alt text.
  - Link titles are dropped. Reference links resolve as CommonMark says.
- **Bare links.**
  - A bare `www.` link gets `http://`.
  - A bare link whose text is over 55 runes gets `display`: the first 39 runes, then ` ... `, then the last 10.
  - `[text](url)` never gets `display`.
- **Embeds.** When `opt.Autolink` is on, a line that holds only one bare link becomes an embed block, if `EmbedID` accepts the URL for an embed tag.
  - The paragraph splits around it: `a⏎<link>⏎b` gives `p(a)`, the embed, `p(b)`.
  - This applies in the root, in quotes and in list items.
  - A link with its own text, a link inside a sentence, and a link inside a spoiler stay `url`.
- **Smilies**, when `opt.Smilies` is on:
  - A code matches only between word boundaries. A boundary is the edge of the text, or a character that is not a letter or digit.
  - The longest code wins.
  - Codes never match inside `code`, `pre`, `url`, `email` or an image's alt text.
- **Code blocks.** `pre` takes `lang` from the first word of the info string when the word fits the pattern. Otherwise `pre` has no `lang`.
- **Lists** carry `ordered`, `start` (only when it is not 1) and `tight`. A tight list's items still hold `p` blocks.
- **Markdown off.** The source is plain text. Blank lines split paragraphs, and each line break is `br`. Autolinks, embeds and smilies still follow their own flags.
- **Caps.** Content nested deeper than `MaxDepth` elements or `MaxQuoteDepth` quotes becomes plain text: its text, without the markup.

- [ ] **Step 1: Write the failing tests.**
  - `TestParseBasic`: for each element row of spec 12.2, one input and its expected tree.
  - `TestParseLineBreaks`:
    - `a\nb` gives `p(a, br, b)`.
    - `a\n\nb` gives two `p`.
  - `TestParseSpoiler`:
    - `||x||` gives a spoiler.
    - `||x` gives the text `||x`.
    - `||a **b**||` gives a spoiler holding text and `b`.
    - `||a\nb||` gives one spoiler across a `br`.
    - `||a\n\nb||` gives text.
  - `TestParseEmbeds`:
    - A YouTube link alone gives a `youtube` block.
    - `see https://youtu.be/<id> now` gives a `url`.
    - `[clip](https://youtu.be/<id>)` gives a `url`.
    - `x⏎<post on X>⏎y` gives `p`, `tweet`, `p`.
    - There is one case for each embed tag.
    - A `redd.it` short link gives a `url`.
  - `TestParseRawHTML`: `<script>x</script>` gives text with that exact source.
  - `TestParseDangerousLinks`: `[x](javascript:alert(1))`, `[x](JaVaScRiPt:x)`, `[x](data:text/html,x)`, `![a](data:image/png;base64,x)` and `<javascript:x>` give no `url` and no `img`.
  - `TestParseFlagsOff`:
    - With Markdown off, `**x**` stays text.
    - With Smilies off, there is no `smiley`.
    - With Autolink off, there are no bare links and no embeds.
  - `TestParseSmilies`:
    - `:)` gives a smiley.
    - `` `:)` `` gives code.
    - `a:)b` gives text.
    - `:):)` gives two smilies.
  - `TestParseAutolink`:
    - A short URL gives a `url` with no `display`.
    - A 70-character URL gets the 39 + ` ... ` + 10 `display`.
    - `www.a.example` gets `http://`.
  - `TestParseNestingCaps`: each case finishes in under 200 ms with no panic.
    - 10,000 nested `>`, with quote depth ≤ 10.
    - 50,000 `**` with no closing.
    - 10,000 `[`.
    - 10,000 nested `- ` list markers.
  - `FuzzParse`: it never panics, and `Validate(Parse(s, allOn))` is always nil.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./markup/ -run Parse`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./markup/ && go test ./markup/ -run '^$' -fuzz FuzzParse -fuzztime 60s`. Expected: PASS, and the fuzzer finds nothing.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "markup: Markdown parser with spoilers, embeds and smilies" && git push`

---

### Task 11: Renderer

Revision 1: the element set is Task 14's. This task needs Task 14, not Task 10.

**Files:**
- Create: `markup/render.go`, `markup/render_test.go`

**Interfaces:**
- Consumes: `Node`, `EmbedID`, `CheckLinkURL`, `CheckImageURL` (Task 14).
- Produces:
  - `type RenderContext struct { Smiley func(code string) (Smiley, bool) }` (the final fix wave replaced the first draft, `SmileyURL`).
  - `type Rendered struct { HTML template.HTML; Text string; FrameSrc, ScriptSrc []string }`. `FrameSrc` and `ScriptSrc` are the origins this tree's embeds need, sorted, with no duplicates.
  - `func Render(n *Node, rc RenderContext) Rendered`.

Output. Escape all text and every attribute value.

| Node | HTML |
|---|---|
| `p` | `<p>…</p>`; in an `li` of a `tight` list, the children only |
| `br` | `<br>` |
| `h` | level 1 → `<h3>`, 2 → `<h4>`, 3 → `<h5>`, 4–6 → `<h6>` |
| `hr` | `<hr>` |
| `quote` | `<blockquote>…</blockquote>` |
| `list`, `li` | `<ol>` (with `start="N"` when set) or `<ul>`; `<li>` |
| `pre` | `<pre class="code"><code>…</code></pre>`; with `lang`, `<code class="language-{lang}">` |
| `code` | `<code>…</code>` |
| `b`, `i` | `<strong>`, `<em>` |
| `url` | `<a href="{href}" rel="nofollow ugc noopener">`; the text is `display` when set, else the children |
| `email` | `<a href="mailto:{address, percent-encoded}">` |
| `img` | `<img src="{src}" alt="{alt}" loading="lazy" class="post-img">`; `http://` becomes `https://` |
| `spoiler` | `<span class="spoiler" tabindex="0">…</span>` |
| `smiley` | `<img class="smiley" src="{Smiley(code).Image}" alt="{Smiley(code).Alt, or the code when empty}">`; an unknown code → the code as text |
| unknown element | its children only |

Embeds. Check each entry against the provider's published embed documentation, and cite that page in a comment by the entry. If the docs disagree with this table, follow the docs and say so in the report.

| Tag | HTML | FrameSrc | ScriptSrc |
|---|---|---|---|
| `youtube` | `<iframe class="embed youtube" src="https://www.youtube-nocookie.com/embed/{id}" title="YouTube video" loading="lazy" allow="encrypted-media; picture-in-picture; fullscreen" allowfullscreen referrerpolicy="strict-origin-when-cross-origin"></iframe>` | `https://www.youtube-nocookie.com` | — |
| `vimeo` | `<iframe class="embed vimeo" src="https://player.vimeo.com/video/{id}" title="Vimeo video" loading="lazy" allow="fullscreen; picture-in-picture" allowfullscreen></iframe>` | `https://player.vimeo.com` | — |
| `tweet` | `<blockquote class="twitter-tweet"><a href="https://twitter.com/i/status/{id}">View post on X</a></blockquote>` | `https://platform.twitter.com` | `https://platform.twitter.com` |
| `instagram` | `<blockquote class="instagram-media" data-instgrm-permalink="https://www.instagram.com/p/{id}/"><a href="https://www.instagram.com/p/{id}/">View on Instagram</a></blockquote>` | `https://www.instagram.com` | `https://www.instagram.com` |
| `reddit` | `<blockquote class="reddit-embed-bq"><a href="https://www.reddit.com/r/{sub}/comments/{post}/">View post on Reddit</a></blockquote>` | `https://embed.reddit.com` | `https://embed.reddit.com` |
| `tiktok` | `<iframe class="embed tiktok" src="https://www.tiktok.com/player/v1/{id}" title="TikTok video" loading="lazy" allow="fullscreen" allowfullscreen></iframe>` | `https://www.tiktok.com` | — |
| `threads` | `<blockquote class="text-post-media" data-text-post-permalink="https://www.threads.com/@{user}/post/{post}"><a href="https://www.threads.com/@{user}/post/{post}">View on Threads</a></blockquote>` | `https://www.threads.com` | `https://www.threads.com` |
| `spotify` | `<iframe class="embed spotify" src="https://open.spotify.com/embed/{type}/{id}" title="Spotify" loading="lazy" allow="encrypted-media; fullscreen; picture-in-picture"></iframe>` | `https://open.spotify.com` | — |
| `soundcloud` | `<iframe class="embed soundcloud" src="https://w.soundcloud.com/player/?url=https%3A//soundcloud.com/{path}" title="SoundCloud" loading="lazy"></iframe>` | `https://w.soundcloud.com` | — |

The renderer checks again what `Validate` checks, because a stored tree may predate a rule:
- A `url` or `email` whose value fails renders its children as text, with no link.
- An `img` whose `src` fails renders its alt text.
- An embed whose `id` fails `EmbedID` renders nothing.

`Rendered.Text` is the plain text of everything except `quote` contents and embeds. It feeds `posts.body_text`.

- [ ] **Step 1: Write the failing tests.**
  - `TestRenderTable`: for each row of both tables, a tree in and the exact HTML out.
  - `TestRenderEscapes`: the text `<script>&"` gives `&lt;script&gt;&amp;&#34;`.
  - `TestRenderRejectsDangerousSchemes`: trees built in code, without `Validate`. Each of `url`, `img` and `email` gets `javascript:alert(1)`, `JaVaScRiPt:x`, `data:text/html,x`, `vbscript:x` and `&#106;avascript:x`. The output has no `href` or `src` for any of them.
  - `TestRenderHTTPImageUpgraded`.
  - `TestRenderOrigins`: a tree with two `youtube` and one `tweet` gives `FrameSrc` [platform.twitter.com, youtube-nocookie.com] and `ScriptSrc` [platform.twitter.com].
  - `TestRenderTextExcludesQuotes`.
  - `TestRenderTightList`.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./markup/ -run Render`. Expected: FAIL.
- [ ] **Step 3: Implement.** Write into a `strings.Builder`, with `html.EscapeString` for text and attribute values. Build the tag HTML by hand from the tables. The output never goes through `html/template` again.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./markup/`. Expected: PASS.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "markup: allowlist renderer with embeds" && git push`

---

### Task 12: Tree comparison

Revision 1: the fixtures are Markdown. `FuzzRender` moves here from Task 11. This task needs Tasks 10 and 11.

**Files:**
- Create: `markup/compare.go`, `markup/compare_test.go`, `markup/render_fuzz_test.go`, `markup/testdata/roundtrip/*.md`, `markup/testdata/roundtrip/*.json`

**Interfaces:**
- Consumes: `Node`, `Validate`, `Parse`, `Render` (Tasks 10, 11, 14).
- Produces:
  - `type Diff struct { Path, Want, Got string }`.
  - `func Compare(want, got *Node) []Diff`. It compares kind, name, attributes (as a sorted `k=v` list) and text, depth-first, with an explicit stack. `Path` names each step by element and index, for example `/quote[0]/b[1]`. An empty result means the trees are equal.

A converter from another markup writes Markdown. It runs `Compare(expectedTree, Parse(itsMarkdown, flags))` over every post. This task builds and tests that tool.

- [ ] **Step 1: Write the fixtures and the failing tests.** Hand-write pairs: `<name>.md` (the source) and `<name>.json` (the expected tree). Write one pair for each of these:
  - plain text with line breaks;
  - bold inside a list;
  - a quote whose first line names its author and links to a post;
  - an autolinked long URL;
  - smilies;
  - a YouTube link alone on its line;
  - a link to a post on X inside a sentence;
  - entities in text (`&`, `<`, `"`);
  - escaped asterisks (`\*sigh\*` stays literal);
  - a spoiler;
  - a fenced code block with a language.

  Then write the tests:
  - `TestCompareEqual`: identical trees give no diffs.
  - `TestCompareText`: one text differs, which gives one diff whose path names it.
  - `TestCompareAttrs`: an attribute value differs, and an attribute set differs.
  - `TestCompareShape`: a child is missing.
  - `TestRoundTripFixtures`: for every pair, `Compare(fixtureTree, Parse(source, Options{Markdown: true, Smilies: true, Autolink: true, SmileyList: fixtureSmilies}))` is empty, and the fixture tree passes `Validate`.
  - `FuzzRender`: `Render(Parse(s, allOn))` never panics. The output never contains `<script` (in any case). Every `href` and `src` value in the output passes `CheckLinkURL` or `CheckImageURL` once it is unescaped.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./markup/ -run 'Compare|RoundTrip'`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./... && go test ./markup/ -run '^$' -fuzz FuzzRender -fuzztime 60s`. Expected: PASS across the repo, and CI is green after the push.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "markup: tree comparison and render fuzzing" && git push`

---

### Task 13: Container image, Compose file, and a minimal `serve`

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `compose.yaml`, `.env.example`, `.github/compose.ci.yaml`
- Create: `internal/web/health.go`, `internal/web/health_test.go`
- Modify: `cmd/gobb/main.go` (add `serve` and `healthcheck`), `.github/workflows/ci.yml` (add the `image` job), `README.md` (add "Run with Docker Compose")

**Interfaces:**
- Consumes: `config.FromEnv`, `db.Open`, `db.Migrate` (Task 3).
- Produces:
  - `type web.Pinger interface { Ping(ctx context.Context) error }` (satisfied by `*pgxpool.Pool`); `func web.Health(p web.Pinger) http.Handler` — `GET` → 200 `ok` when the ping succeeds within 2 s, else 503 `db unavailable`; other methods → 405.
  - `gobb serve` — loads config, opens the pool, runs `db.Migrate`, serves `GET /healthz` on `Listen`. On SIGTERM or SIGINT it shuts down gracefully within 25 s (under compose's 30 s grace). Plan 3 builds the real site on this.
  - `gobb healthcheck` — `GET http://127.0.0.1:<port of GOBB_LISTEN>/healthz` with a 2 s timeout; exit 0 on 200, else 1. It exists because the static image has no shell or curl.

Image and Compose:
- `Dockerfile`:
  - build stage `cgr.dev/chainguard/go:latest`: `go mod download` before copying the source (layer cache), then `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/gobb ./cmd/gobb`, and create `/out/data`;
  - runtime `cgr.dev/chainguard/static:latest`: binary at `/usr/bin/gobb`, `/out/data` copied to `/var/lib/gobb` with `--chown=nonroot:nonroot` before `USER nonroot`, `VOLUME ["/var/lib/gobb"]`;
  - `ENV GOBB_LISTEN=0.0.0.0:8080 GOBB_DATA_DIR=/var/lib/gobb`, `EXPOSE 8080`, `ENTRYPOINT ["/usr/bin/gobb"]`, `CMD ["serve"]`.
- `compose.yaml` (project name `gobb`):
  - **`postgres`:** image `postgres:16`; env `POSTGRES_USER=gobb`, `POSTGRES_DB=gobb`, `POSTGRES_PASSWORD=${POSTGRES_PASSWORD:?set POSTGRES_PASSWORD in .env}`; `command: ["postgres", "-c", "shared_buffers=64MB"]`; volume `gobb-pg:/var/lib/postgresql/data`; healthcheck `pg_isready -U gobb -d gobb`; `stop_grace_period: 30s`; `restart: unless-stopped`.
  - **`gobb`:** `image: ghcr.io/evanwtf/gobb:${GOBB_VERSION:-latest}` and `build: .` (pull or build, spec 15); `depends_on: postgres: condition: service_healthy`; env:
    - `GOBB_DSN=postgres://gobb:${POSTGRES_PASSWORD}@postgres:5432/gobb?sslmode=disable`;
    - `GOBB_BASE_PATH=${GOBB_BASE_PATH:-/}`;
    - `GOBB_TRUSTED_PROXIES=${GOBB_TRUSTED_PROXIES:-10.231.0.1/32}`.

    Ports `127.0.0.1:${GOBB_PORT:-8080}:8080`; volume `gobb-data:/var/lib/gobb`; healthcheck `["CMD", "/usr/bin/gobb", "healthcheck"]`; `stop_grace_period: 30s`; `restart: unless-stopped`.
  - **Network:** the default network is pinned to subnet `10.231.0.0/24`, so a host reverse proxy always appears from the gateway `10.231.0.1`. A comment says to change the subnet and `GOBB_TRUSTED_PROXIES` together if it collides.
- `.env.example`: `GOBB_VERSION`, `POSTGRES_PASSWORD`, `GOBB_BASE_PATH`, `GOBB_TRUSTED_PROXIES`, `GOBB_PORT`, with no real values.
- `.github/compose.ci.yaml`: an override with `ports: !override ["127.0.0.1::8080"]` (random host port) and `networks: default: ipam: !reset {}` (no pinned subnet), so concurrent runs on the shared runner do not collide.

- [ ] **Step 1: Write the failing tests**
  - `TestHealthOK` — a fake pinger returning nil → 200, body `ok`.
  - `TestHealthDBDown` — a pinger returning an error → 503.
  - `TestHealthSlowDB` — a pinger that blocks past 2 s → 503 within 2.5 s.
  - `TestHealthMethod` — `POST` → 405.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/web/`. Expected: FAIL (undefined).
- [ ] **Step 3: Implement** `web.Health`, `serve`, `healthcheck`, the Dockerfile, `compose.yaml`, `.env.example`, and the CI override.
- [ ] **Step 4: Verify locally**

Run: `go test -race ./internal/web/ && docker build -t gobb:dev . && docker run --rm gobb:dev version`
Expected: PASS, then `gobb 0.1.0`.
Run: `cp .env.example .env && sed -i '' 's/^POSTGRES_PASSWORD=.*/POSTGRES_PASSWORD=dev-only/' .env && docker compose up -d --build --wait && curl -fsS http://127.0.0.1:8080/healthz && docker compose down -v`
Expected: `--wait` returns once both services are healthy; curl prints `ok`. (Run `colima start` first if Docker is not running.)

- [ ] **Step 5: Add the `image` job to CI**

A job `image` with the same fork guard, `runs-on: [self-hosted, Linux, X64]` and `timeout-minutes: 15`, that:
1. Runs `docker build -t gobb:ci-$GITHUB_RUN_ID .` and checks that `docker run --rm gobb:ci-$GITHUB_RUN_ID version` prints `gobb 0.1.0` — the exact production target, run, not only built.
2. Runs `POSTGRES_PASSWORD=ci docker compose -p gobb-ci-$GITHUB_RUN_ID -f compose.yaml -f .github/compose.ci.yaml up -d --build --wait`.
3. Always runs, even after a failure: `docker compose -p gobb-ci-$GITHUB_RUN_ID ... down -v` and `docker image rm gobb:ci-$GITHUB_RUN_ID`.

- [ ] **Step 6: Verify on GitHub and commit**

```bash
git add -A && git commit -m "deploy: Chainguard image, compose file, and health check" && git push
gh run watch --exit-status $(gh run list --workflow ci.yml --limit 1 --json databaseId -q '.[0].databaseId')
```

Expected: the `check` and `image` jobs both pass.

---

### Task 14: The Markdown tree

Revision 1. This task reshapes Task 9's package for Markdown.

**Files:**
- Rename: `bbcode/` to `markup/` (`git mv`). The package is `markup`; the import path is `github.com/evanwtf/gobb/markup`.
- Modify: `markup/doc.go`, `markup/node.go`, `markup/validate.go`, `markup/embedid.go` and their tests; `docs/changelog.md`

**Interfaces:**
- Consumes: Task 9's `Node`, `Kind`, `Validate`, `Decode`, `EmbedID`, `MaxDepth`, `MaxQuoteDepth`.
- Produces:
  - The same names, in package `markup`, with the schema below.
  - `func CheckLinkURL(s string) bool` and `func CheckImageURL(s string) bool`: the one URL rule. `Validate`, `Parse` and `Render` all use it.
  - `func IsEmbed(name string) bool`.

The schema:
- The blocks are `p`, `h`, `hr`, `quote`, `list`, `pre` and the embeds.
- The inlines are text, `b`, `i`, `code`, `br`, `url`, `email`, `img`, `spoiler` and `smiley`.
- The root holds blocks.

| Element | Attributes | Children |
|---|---|---|
| `p` | none | inlines |
| `h` | `level` required, `1` to `6` | inlines |
| `hr`, `br` | none | none |
| `quote` | none | blocks |
| `list` | `ordered` optional (`true`); `start` optional, 1–9 digits with no leading zero, only with `ordered`; `tight` optional (`true`) | `li` only |
| `li` | none | blocks; only inside `list` |
| `pre` | `lang` optional, `^[A-Za-z0-9_+.#-]{1,32}$` | text only |
| `code` | none | text only |
| `b`, `i` | none | inlines |
| `url` | `href` required (`CheckLinkURL`); `display` optional, 1–255 characters | inlines, except `url` and `email` |
| `email` | `address` required (Task 9's rule) | inlines, except `url` and `email` |
| `img` | `src` required (`CheckImageURL`); `alt` optional, up to 1,000 characters | none |
| `spoiler` | none | inlines, except `spoiler` |
| `smiley` | `code` required, 1–32 characters | none |
| `youtube`, `vimeo`, `tweet`, `instagram`, `reddit`, `tiktok`, `threads`, `spotify`, `soundcloud` | `id` required, and `EmbedID(name, id)` returns it unchanged | none |

Removed: `u`, `color`, `size`, `attachment`, `shout`, `comic`, the `quote` attributes, `list type`, and the `spoiler` label.

URL rules. Keep Task 9's steps: decode entities, trim, and reject control characters, whitespace and backslashes.
- `CheckLinkURL` accepts any of these:
  - `http` or `https` with a host;
  - `mailto:` followed by an address that passes the `email` rule;
  - a site path: it starts with `/`, but not with `//` or `/\`.
- `CheckImageURL` accepts `http` or `https` with a host, or a site path.

`MaxDepth` stays 32. `MaxQuoteDepth` stays 10, and it now counts `quote` blocks.

`EmbedID` adds the entries below. URL forms take `http` or `https` and an optional `www.` or a listed subdomain. They ignore query strings unless the table names one.

| Tag | Canonical ID | URL forms |
|---|---|---|
| `youtube` | as Task 9 | Task 9's forms, plus `m.youtube.com`, `music.youtube.com` and `youtube.com/live/<id>` |
| `tweet` | as Task 9 | Task 9's forms, plus `mobile.twitter.com` |
| `instagram` | as Task 9 | `instagram.com/p/<id>`, `/reel/<id>`, `/tv/<id>` |
| `reddit` | `<subreddit>/<post>`: `^[A-Za-z0-9_]{2,21}/[a-z0-9]{1,12}$` | `reddit.com/r/<sub>/comments/<post>`, with an optional `/<slug>` and trailing slash; also on `old.` and `new.` |
| `tiktok` | `^[0-9]{1,20}$` | `tiktok.com/@<user>/video/<id>` |
| `threads` | `<user>/<post>`: `^[A-Za-z0-9_.]{1,30}/[A-Za-z0-9_-]{1,64}$` | `threads.net/@<user>/post/<post>`, `threads.com/@<user>/post/<post>` |
| `spotify` | `<type>/<id>`: type one of `track album playlist episode show`; id `^[A-Za-z0-9]{22}$` | `open.spotify.com/<type>/<id>`, also with an `intl-<lang>/` segment first |
| `soundcloud` | `<user>/<track>` or `<user>/sets/<set>`; each segment `^[a-z0-9_-]{1,100}$` | `soundcloud.com/<user>/<track>`, `soundcloud.com/<user>/sets/<set>` |

Short links need a server lookup, so `EmbedID` returns false for them: `redd.it`, `vm.tiktok.com`, `spotify.link` and `on.soundcloud.com`. `youtu.be` keeps working, as in Task 9.

- [ ] **Step 1: Update the tests first.**
  - `TestTreeJSONRoundTrip` and `TestValidateAccepts`: one valid tree for each row of the schema.
  - `TestValidateRejects`: Task 9's cases that still apply, and these:
    - a removed element (`u`, `color`, `shout`) gives an error that names it;
    - text directly in the root;
    - a `p` inside `b`;
    - an `li` that holds text directly;
    - `h` with level 7;
    - `list` with `start=0`, and `start` without `ordered`;
    - `pre` with an element child;
    - `img` with `src` `//evil.example/x`;
    - `url` with `href` `/\evil.example`;
    - a `spoiler` inside a `spoiler`;
    - an embed inside a `p`.
  - `TestCheckURLs`:
    - Accepted: `https://a.example/x`, `/t/5`, `/files/a.png`, `mailto:a@b.example`.
    - Rejected: `//a.example`, `/\a`, `javascript:x`, `mailto:`, `mailto:a`, `ftp://a.example`, `data:image/png;base64,x`.
    - `CheckImageURL` also rejects every `mailto:` value.
  - `TestValidateDeepTree`:
    - 33 nested `b` give an error.
    - 11 nested `quote` give an error.
    - A chain 100,000 deep gives an error in under 100 ms.
  - `TestEmbedID`: for each tag, a canonical ID, each URL form and one invalid value; also each short link listed above, which returns false.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./markup/`. Expected: FAIL.
- [ ] **Step 3: Implement.** `doc.go` states three things:
  - the package's purpose;
  - that the schema is the contract for converters;
  - the renderer contract: every attribute value is HTML-escaped on output, and `mailto:` addresses are percent-encoded.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./...`. Expected: PASS. `git grep -n bbcode -- '*.go'` prints nothing.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "markup: reshape the tree for Markdown" && git push`

---

### Task 15: Markdown names in the schema and permissions

Revision 1. **Worker: Haiku, max effort.**

**Files:**
- Modify: `internal/db/migrations/0001_schema.sql`, `internal/db/schema_test.go`, `internal/acl/options.go`, `internal/acl/acl_test.go`, `docs/changelog.md`

`0001_schema.sql` is not released yet, so edit it in place:
- In every table, rename `enable_bbcode` to `enable_markdown` and `enable_magic_url` to `enable_autolink`. `enable_smilies` keeps its name.
- Drop the `tag_aliases` table and `TestTagAliasTarget`.

Permissions: rename `post.bbcode` to `post.markdown` and `pm.bbcode` to `pm.markdown`. The counts stay 23 / 6 / 19.

- [ ] **Step 1: Update the tests.**
  - The table list has no `tag_aliases`.
  - A test that `posts` and `pms` have `enable_markdown` and `enable_autolink`.
  - The registry test names `post.markdown` and `pm.markdown`.
- [ ] **Step 2: Run them to verify they fail.** Run: `go test ./internal/db/ ./internal/acl/`. Expected: FAIL.
- [ ] **Step 3: Edit** the schema and the registry.
- [ ] **Step 4: Run them to verify they pass.** Run: `go test -race ./internal/db/ ./internal/acl/`. Expected: PASS. `git grep -n -i 'bbcode\|magic_url\|tag_alias' -- internal cmd` prints nothing.
- [ ] **Step 5: Commit and push.** `git add -A && git commit -m "db: Markdown names in the schema and permissions" && git push`

---

## End of plan

When Task 13 is green in CI, the Opus reviewer reviews the whole plan's work against the spec sections named in the header. Then the next plan (Import) is written against the code as it stands.
