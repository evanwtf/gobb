# AGENTS.md

Instructions for any agent or person who works in this repo. `CLAUDE.md` and
`GEMINI.md` are symlinks to this file.

## Clean room

gobb is a clean-room implementation.

- Work only from this repo: its docs, its plans, its code and its tests. Public
  standards are fine (RFCs, the Go and PostgreSQL docs, published hash and
  encoding specifications).
- Do not read, search for, or copy the source, templates, stylesheets or images of
  any other forum software. This includes "just checking how X does it".
- If a task needs a fact about another system, stop and ask the owner. Do not look
  it up.

## This repo is public

Do not commit hostnames, IP addresses, home-directory paths, secret values, or
secret-manager item names. This covers files, commit messages, issues, PRs and
comments. Describe a deployment by its role ("the production host"), not its
address.

Never approve a workflow run for a pull request from a fork. A maintainer pushes
the branch to this repo instead. The shared self-hosted runner runs the code of
any workflow it is given, and a fork PR brings its own workflow file.

Do not commit forum data: no database dumps, no attachments, no avatars, no real
usernames or posts. Tests use fixtures that you write by hand.

A private denylist of identifying words runs in pre-commit (from a local, git-ignored
`.public-denylist`) and in CI (from a repository secret). A hit prints the file and
line only. Do not try to work out what the list contains; reword the line.

## Commits

- Subject: `area: imperative summary`.
- Do not put agent session URLs, session IDs, `Claude-Session:` trailers or
  `Co-Authored-By` agent trailers in commits, PRs, issues or comments.
- Work lands on `main` until the forum has real users. After that, use pull
  requests.

## Commands

```sh
go build ./cmd/gobb && ./gobb version                                    # build and smoke-run
go test ./...                                                            # tests
uvx pre-commit install --hook-type pre-commit --hook-type commit-msg     # once per clone
uvx pre-commit run --all-files                                           # all hooks
sh scripts/check-public.sh                                               # the public-repo guard alone
./gobb serve                                                             # run the server; needs GOBB_DSN, see the README
./gobb import [--replace] <bundle>                                       # load a bundle (directory or .tar.gz) into an empty database; --replace empties the database first
./gobb perms export > perms.jsonl                                        # write the effective permissions as JSON Lines
./gobb user reset-password <name>                                        # new random password, printed once on stdout; ends the member's sessions
./gobb user ban <name> [-reason TEXT]                                    # ban a member (no end date) and end the sessions
./gobb user unban <name>                                                 # remove the member's bans that have not ended
```

Tests that need PostgreSQL read `GOBB_TEST_DATABASE_URL` and skip when it is unset.
A skipped test is not a pass: CI sets the variable and fails on any skip.

## Conventions

General conventions live in https://github.com/evanwtf/baseline (private).
