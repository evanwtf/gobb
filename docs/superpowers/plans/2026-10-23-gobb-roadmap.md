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
