# gobb roadmap

The design (`docs/superpowers/specs/2026-10-23-gobb-design.md`) is one product, but it
has seven parts that build on each other. Each part gets its own plan. A plan is written
only after the plan before it has landed, against the code as it exists then, so no plan
guesses at interfaces that are not built yet.

Execution for every plan: subagent-driven. Sonnet at high effort implements each task
(Haiku at max effort for tasks marked mechanical). A fresh Opus reviewer at high effort
gates each task, and reviews the whole plan's work at the end. Every worker and reviewer
follows the clean-room rules in `AGENTS.md`.

| # | Plan | Delivers | Needs |
|---|---|---|---|
| 1 | **Foundation** | Repo guards, CI, config, database and migrations, the release-1 schema, the test-first cores (legacy and gobb password checks, permission resolution, the public `markup` package: Markdown parser, tree, validation, renderer, comparison), and the container: `Dockerfile`, `compose.yaml`, and an image that CI builds and runs on every push. | — |
| 2 | **Import** | The public `bundle` package (types, reader, writer, validation), `gobb import` (design 8.1, 8.2), and `gobb perms export` (8.3). Tested with hand-written bundles. | 1 |
| 3 | **Read side** | `gobb serve`: layout and the Slate schemes, login and sessions, board index, forum, topic, profile, member list, team, "new since your last visit", search, legacy routes and file paths, 410 for gone users, security headers and CSP. | 1, 2 |
| 4 | **Write side** | Posting (new, reply, quote, edit, delete), image attachments, polls, private messages, profile editing, topic watches, the email queue and SMTP, password reset. | 3 |
| 5 | **Moderation and admin** | In-place moderation, the Spammer action with undo, bans, the approval queue, settings, the permissions grid, groups, forums, smilies, the log, header upload, static pages. | 4 |
| 6 | **Release 1 and cutover** | The tag-driven release workflow with checks: binaries plus a multi-arch image in GitHub Container Registry. Then the timed rehearsal on the production host under Docker Compose, and the cutover itself. Host steps live in the owner's private deployment notes. | 5 |
| 7 | **Release 2: open sign-up** | Registration with every design section 10 defense, the holding area, and the new-members admin page. | 6 |

Work outside this repo, by the owner's side:

- Before Plan 1 Task 6: the legacy password vectors file (`internal/password/testdata/`).
- After Plan 2: the first board's converter, which writes a bundle and runs the checks
  in design section 8.3. It is private.

The look comes from a static mockup the owner approved. It shows real member data, so
it stays private. Plan 3 rebuilds it here from scratch with made-up data.

## Carried from Plan 1

Decisions and limits that Plan 1 found and later plans must honor:

- **Plan 2 (import).**
  - Load the bundle in one transaction with `set constraints all deferred`. Every foreign key is deferrable, because the last-post pointers between forums, topics and posts form a cycle.
  - Cap the size of each post that the bundle reader accepts. `Validate` has no node-count or length cap.
  - Refuse NUL in any source text. PostgreSQL cannot store it.
  - Refuse a grant whose scope does not fit its option: a Forum option granted board-wide, or a Board option granted on a forum. `acl.Resolve` ignores such a grant silently.
- **Plan 3 (read side).**
  - Limit concurrent logins and the size of login requests. A stored argon2id hash may use up to 256 MiB.
  - Keep every GET free of side effects. Post images may point at any path on the site.
  - Set server read, write and idle timeouts, and a header-size limit.
  - Bound `pool.Close` inside the 25 s shutdown window. Let a second signal force an exit.
  - The CSP for an X embed also needs `connect-src` and `img-src` for X's syndication hosts.
- **Plan 4 (write side).**
  - Cap a post body at 60,000 bytes, and rate-limit posting and preview per member. goldmark's worst case is about 1 s at that size (a repeated `[a](`). Parse a post once, when it is saved, never when it is viewed.
  - The quote button inserts `> **Name** [wrote](/p/N):` and then a `>` line, so the attribution is its own paragraph.
  - Set `enable_sig` from the member's preference on every insert. Its default is false.
  - YouTube start times can become an optional `start` attribute on `youtube`. This is additive, so stored trees stay valid.
- **Plan 5 (moderation).** A hard delete of a topic or a user must first remove the rows that depend on it. Most foreign keys do not cascade.
- **Plan 6 (release).**
  - Add a test that pins the sha256 of each released migration.
  - The release workflow pushes the image to GitHub Container Registry and defines the `GOBB_VERSION` tag format.
  - CI compares `gobb version` against `internal/version`, not a hard-coded string.
