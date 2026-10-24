# gobb

A small forum server in Go.

## Bootstrap

```sh
brew install go            # or https://go.dev/dl/ — Go 1.27
git clone https://github.com/evanwtf/gobb && cd gobb
go build ./cmd/gobb && ./gobb version
go test ./...
uvx pre-commit install --hook-type pre-commit --hook-type commit-msg
```

## Run with Docker Compose

The stack is gobb and PostgreSQL 16. Inside the container, gobb listens on `0.0.0.0`.
Compose publishes the port on `127.0.0.1` of the host only. Put a reverse proxy in
front of it.

```sh
cp .env.example .env
# Set POSTGRES_PASSWORD in .env.
docker compose up -d --build --wait
curl -fsS http://127.0.0.1:8080/healthz
```

`up --build` builds the image from this checkout. To run a published image instead, set
`GOBB_VERSION` in `.env` to a release tag, or leave it at `latest`, once a release exists. Then run
`docker compose pull && docker compose up -d --wait`. The image comes from
`ghcr.io/evanwtf/gobb`.

`gobb serve` applies the pending migrations at startup, then serves the forum and
`GET /healthz`. It stops cleanly on SIGTERM. Set `GOBB_PORT` in `.env` if port 8080 is
busy.

The compose network uses the subnet `10.231.0.0/24`, so a reverse proxy on the host
reaches gobb from `10.231.0.1`, the default in `GOBB_TRUSTED_PROXIES`. If the subnet
collides with another network, change the subnet in `compose.yaml` and
`GOBB_TRUSTED_PROXIES` together.

To stop the stack and keep the data, run `docker compose down`. To delete the data
too, run `docker compose down -v`.

## Import a bundle

`gobb import [--replace] <bundle>` loads a bundle into the database and the data
volume. The bundle is a directory or a `.tar.gz` file. Put the flag before the path.
The format is in [docs/bundle.md](docs/bundle.md), and
[docs/example-bundle/](docs/example-bundle/) is a small example.

A bundle, a database dump and a permission export hold real member data. Keep them in
a folder outside this clone, so that git and the image build never see them. A converter
packs a bundle directory like this:

```sh
export DIR=/path/to/bundle-directory
export WORK="$HOME/gobb-import"
export BUNDLE="$WORK/bundle.tar.gz"
mkdir -p "$WORK"
tar -C "$DIR" -czf "$BUNDLE" .
chmod 0644 "$BUNDLE"
```

On macOS, write `COPYFILE_DISABLE=1 tar -C "$DIR" -czf "$BUNDLE" .`. Without it,
macOS `tar` adds `._` files, and the importer refuses them.

Stop the server first. The import lock keeps two imports apart, but it does not stop
the server from writing to the board during an import.

This recipe takes a `.tar.gz` file. `$BUNDLE` must be the absolute path of a file that
exists. A relative name makes Compose create a named volume, and a path that does not
exist makes Compose create an empty directory. Both give a confusing error.

```sh
docker compose stop gobb
docker compose run --rm -v "$BUNDLE:/import/bundle.tar.gz:ro" gobb import /import/bundle.tar.gz
docker compose up -d --wait
```

Use `up -d --wait` to restart the server. `start` fails after a `docker compose down`.

For a bundle that is a directory, mount the directory at `/import/bundle` and pass that
path:

```sh
export DIR=/absolute/path/to/bundle-directory
docker compose run --rm -v "$DIR:/import/bundle:ro" gobb import /import/bundle
```

The container runs as the `nonroot` user, so the bundle must be readable by everyone
(mode 0644 for a file, 0755 for a directory). The command applies the pending
migrations, then prints the number of rows it loaded from each data file, and the number
of files it staged.

The import refuses a database that holds data. `--replace` empties every table, and that
includes sessions and logs. Take a dump first. The dump is a plain SQL file, and you
keep it. If the dump fails, do not go on:

```sh
docker compose exec -T postgres pg_dump -U gobb -d gobb > "$WORK/gobb-before-replace.sql" || echo "pg_dump failed; do not run --replace"
```

Then add `--replace`. It moves the old files to `.replaced-<time>/` in the data volume.
It deletes nothing on disk.

```sh
docker compose run --rm -v "$BUNDLE:/import/bundle.tar.gz:ro" gobb import --replace /import/bundle.tar.gz
```

If the import is killed after the commit, the rows are in the database and the files
are not in place. A re-run then says that the database has rows. Run it again with
`--replace`.

The `.replaced-*` folders stay in the data volume until you delete them. An import that
fails to move its files, or that is killed, also leaves a `.import-*` folder. Check the
new board first. The gobb image
has no shell, so delete the folders with a container that has one:

```sh
docker run --rm -v gobb_gobb-data:/data --entrypoint sh postgres:16 \
  -c 'rm -rf /data/.import-* /data/.replaced-*'
```

The data directories (`attachments`, `avatars`, `smilies`, `ranks` and `header`) must be
real directories, not symlinks. The import replaces a symlink with a real directory.

To check the permissions after an import, write them as JSON Lines to a file. The
`-T` flag keeps Compose from adding a terminal, which would change the line endings.
If the command fails, it writes its error into the file and exits with a code other than
0. Check the exit code before you use the file.

```sh
docker compose run --rm -T gobb perms export > "$WORK/perms.jsonl" || echo "perms export failed; do not use perms.jsonl"
```

## Run the server

`gobb serve` needs a PostgreSQL database and a data directory. It applies the pending
migrations at start, so a new database needs no `gobb migrate` first. A new database
holds no board: `gobb import` loads one (see "Import a bundle"). Until then every page
answers 503 and says to run `gobb import`, and `/healthz` answers 200. The server reads
the legacy route table once at start, so restart it after an import.

The server reads these variables:

| Variable | Meaning | Default |
|---|---|---|
| `GOBB_DSN` | PostgreSQL connection string. Required. | none |
| `GOBB_LISTEN` | Address to bind. | `127.0.0.1:8080` |
| `GOBB_DATA_DIR` | Directory for avatars, smilies, ranks, the header image and attachments. | `/var/lib/gobb` |
| `GOBB_BASE_PATH` | URL path prefix, such as `/forum/`. | `/` |
| `GOBB_TRUSTED_PROXIES` | Comma-separated CIDRs of the proxies to trust. | `127.0.0.1/32,::1/128` |
| `GOBB_COOKIE_SECURE` | `true` or `false`. Mark the cookies Secure. | `true` |

Run it from a clone, against a local database. The import uses the same variables:

```sh
export GOBB_DSN='postgres://USER@127.0.0.1:5432/gobb?sslmode=disable'
export GOBB_DATA_DIR="$HOME/gobb-data"
export GOBB_LISTEN=127.0.0.1:8180
go build -o gobb ./cmd/gobb
./gobb import "$BUNDLE"      # once, into an empty database
./gobb serve
curl -fsS "http://$GOBB_LISTEN/healthz"
```

Open `http://127.0.0.1:8180/` in a browser. Add `?scheme=slate-deep` or
`?scheme=slate-light` to the address to see one page in another color scheme. The
board's `default_color_scheme` setting picks the scheme for everyone else.

### Sign in

Members sign in at `/login` with a username or an email address, and the password. The
box "Remember me" keeps the session for a year; without it the session lasts 24 hours.
A guest sees the pages that the guests group may read. Search and the member pages need
a permission, and the board decides who holds it.

To check a sign-in from a shell, run the smoke script against a running server that holds
`docs/example-bundle/`. It fetches the pages, signs in as the made-up member `alice`,
and exits at the first failure. It talks plain http, so the server must run with
`GOBB_COOKIE_SECURE=false` (see "Sign in over plain HTTP"); with `true`, the sign-in
check fails because curl does not send the `Secure` cookie back:

```sh
export BASE=http://127.0.0.1:8180
sh scripts/smoke-serve.sh "$BASE"
```

### Sign in over plain HTTP

The session cookie is `Secure` by default, and a browser sends a `Secure` cookie only
over HTTPS. To try a private copy on `http://127.0.0.1`, turn the flag off for that run.
In a shell:

```sh
export GOBB_COOKIE_SECURE=false
./gobb serve
```

With Docker Compose, set `GOBB_COOKIE_SECURE=false` in `.env`, then run
`docker compose up -d --wait` again.

Keep it `true` for any copy that people reach over a network. Every form post must come
from the same origin: the browser's `Origin` or `Sec-Fetch-Site` header is checked
against the `Host` header. A reverse proxy has to pass the `Host` header of the visitor
on to gobb.

### Old addresses

A bundle carries the address patterns of the old board (`legacy_routes`) and the paths
of its old files (`legacy_files`). A request that no page of gobb answers is matched
against them, and the answer is a 301 to the new address: an old topic link goes to
`/t/{id}`, with `?page=` when the old offset points past the first page. An old profile
link of a removed member answers 410. A link to something the visitor may not see
answers the same 404 as a link to something that does not exist, so an old link never
shows that a hidden topic exists. An address that matches nothing answers 404.

With the example bundle, `/old/thread.cgi?view=full&id=1` answers 301 to `/t/1`, and
`/old/about.cgi` answers 301 to `/pages/about`:

```sh
curl -sS -o /dev/null -D - "$BASE/old/thread.cgi?view=full&id=1"
```

### Sign-in limits

Failed sign-ins are limited. One client address may fail 10 times in 15 minutes (a
client on IPv6 counts as its whole /64), and one typed name may fail 20 times in an
hour. Past a limit, the sign-in page answers 429 and says how long to wait. Notes for
the operator:

- The name limit counts the name as typed, after cleaning. A username and an email
  address for one account are two names, so that account has two budgets.
- A name that is over its limit still works from an address that the account has used
  before. The address limit still applies there.
- The counts live in memory. A restart sets them to zero.
- Behind a reverse proxy, `GOBB_TRUSTED_PROXIES` must name the proxy. Without it,
  every visitor has the address of the proxy and shares one budget of 10 failures.
  The same mistake turns off the name limit for everyone: every member's known
  address is then the address of the proxy.
- A failed sign-in takes at least 400 ms, even when the client closes its end of the
  connection early. This holds for every failure after the limits pass: a wrong
  password, a busy server, an inactive account and a ban.
- Password checks share 128 MiB of memory. Every check holds the same share,
  whatever the kind of hash, so neither the wait nor the answer tells a stranger
  which names have accounts or which accounts have old hashes. The share follows the
  most costly stored hash, which the server reads at start. With no hash above the
  cost of a new gobb hash (19 MiB), six checks run at once. On a board with imported
  64 MiB argon2id hashes, two checks run at once. The start-up log line
  `login: password check charge` gives the share in KiB and the number of checks at
  once. A sign-in replaces an old hash with a gobb hash, so the share falls at a
  later start, when no costly hash is left. A check that finds no room for 5
  seconds answers 503.

Press Ctrl-C or send SIGTERM to stop. The server finishes the open requests and closes
the database pool, inside 20 seconds. A second signal exits at once.

## Status

Plan 1, the foundation, is built. It ships the repo guards and CI, the configuration,
the database pool and migrations, the release-1 schema, legacy and gobb passwords, the
permission registry, the public `markup` package (Markdown parser, renderer, embeds and
tree comparison), and the container image with Compose and a health check. Plan 2 adds
the bundle format and the importer. Plan 3 is under way: `gobb serve` shows the board
index, and the other forum pages follow one slice at a time. The design is in
[docs/superpowers/specs/2026-10-23-gobb-design.md](docs/superpowers/specs/2026-10-23-gobb-design.md),
and the build order is in
[docs/superpowers/plans/2026-10-23-gobb-roadmap.md](docs/superpowers/plans/2026-10-23-gobb-roadmap.md).

## Scope

- One Go binary serves the forum as server-rendered HTML.
- The forum stores its data in PostgreSQL, in its own database, with its own table design.
- Members write Markdown, with `||spoilers||` and embeds for links to video and social
  sites. Pages render from a parsed tree through an allowlist.
- Groups and roles grant permissions per forum or board-wide, with YES, NO and NEVER.
- Data moves in through a documented bundle format, so any converter can feed it.
- It runs under Docker Compose, or as a single binary behind a reverse proxy.

gobb is a clean-room implementation. It contains no code, templates, stylesheets or
images from any other forum software.

## License

MIT. See [LICENSE](LICENSE).
