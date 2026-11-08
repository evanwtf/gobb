# gobb

A small forum server in Go. Members read and write topics, keep their own settings on
the account page (time zone, date format, avatar, "About you", birthday, signature), and
send each other private messages.

## Bootstrap

```sh
brew install go            # or https://go.dev/dl/ — Go 1.27
git clone https://github.com/evanwtf/gobb && cd gobb
go build ./cmd/gobb && ./gobb version
go test ./...
uvx pre-commit install --hook-type pre-commit --hook-type commit-msg
```

## Run with Docker Compose

The stack is gobb and PostgreSQL 18. Inside the container, gobb listens on `0.0.0.0`.
Compose publishes the port on `127.0.0.1` of the host only. Put a reverse proxy in
front of it.

```sh
cp .env.example .env
# Set POSTGRES_PASSWORD in .env.
docker compose up -d --build --wait
curl -fsS http://127.0.0.1:8080/healthz
```

`up --build` builds the image from this checkout. To run a published image instead, set
`GOBB_VERSION` in `.env` to a version without the `v` (for example `0.2.0`; the release
tag is `v0.2.0`), or leave it at `latest`. Then run
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

`gobb import [--replace] [--thread-messages] <bundle>` loads a bundle into the database
and the data volume. The bundle is a directory or a `.tar.gz` file. Put the flags before
the path. `--thread-messages` joins a private-message reply that the bundle links to no
conversation by its people and subject, the rule of `gobb messages thread`. It is off by
default, because two unrelated talks can share a subject.
The format is in [docs/bundle.md](docs/bundle.md), and
[docs/example-bundle/](docs/example-bundle/) is a small example. gobb 0.2.0 reads format
version 2 and refuses a bundle of version 1.

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
docker run --rm -v gobb_gobb-data:/data --entrypoint sh postgres:18 \
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

## Back up and upgrade

Back up before every upgrade. The backup is two files: a dump of the database and an
archive of the data volume. Run these commands in the folder of `compose.yaml`.

A dump and an archive hold real member data. Keep them on the server, in a folder
outside this clone and outside the image build, and do not commit them or send them
to a service that you do not run. Copy them to another machine only over a link that
you control.

```sh
export BACKUP_DIR="$HOME/gobb-backups"
export STAMP=$(date +%Y%m%d-%H%M%S)
umask 077
mkdir -p "$BACKUP_DIR"
```

1. **Dump the database.** `pg_dump -Fc` writes the compressed custom format. Run it in
   the `postgres` container, and write the file on the host. The `-T` flag keeps
   Compose from adding a terminal, which would change the bytes. A failed dump is
   deleted, so that it cannot look like a backup:

   <!-- block: dump -->
   ```sh
   docker compose exec -T postgres pg_dump -U gobb -d gobb -Fc > "$BACKUP_DIR/gobb-$STAMP.dump" || { rm -f "$BACKUP_DIR/gobb-$STAMP.dump"; echo "pg_dump failed; do not upgrade"; }
   ```

2. **Archive the data volume.** The gobb image has no shell, so a container that has
   `tar` reads the volume of the running gobb container, read-only. A failed archive
   is deleted too:

   <!-- block: archive -->
   ```sh
   docker run --rm --volumes-from "$(docker compose ps -q gobb):ro" -v "$BACKUP_DIR:/backup" \
     --entrypoint sh postgres:18 \
     -c 'tar czf "/backup/$1" -C /var/lib/gobb . && chown "$2" "/backup/$1" && chmod 600 "/backup/$1"' \
     sh "gobb-data-$STAMP.tar.gz" "$(id -u):$(id -g)" || { rm -f "$BACKUP_DIR/gobb-data-$STAMP.tar.gz"; echo "tar failed; do not upgrade"; }
   ```

3. **Check the files before you upgrade.** A dump that is a few kilobytes for a board
   with members means that something failed. Compare its size with the last dump, and
   list its contents:

   <!-- block: check -->
   ```sh
   ls -l "$BACKUP_DIR/gobb-$STAMP.dump" "$BACKUP_DIR/gobb-data-$STAMP.tar.gz"
   docker compose exec -T postgres pg_restore --list < "$BACKUP_DIR/gobb-$STAMP.dump" | wc -l
   ```

4. **Upgrade.** Set `GOBB_VERSION` in `.env` to the new version without the `v`, for
   example `0.2.0` (the release tag is `v0.2.0`, the image tag is `0.2.0`), or leave it
   at `latest`. Write down the version that you leave: a rollback needs it. Pull the
   image and start the stack. gobb applies the new migrations at start. A migration is
   not undone when you go back to an older image, so to go back you restore the dump
   (below).

   ```sh
   docker compose pull && docker compose up -d --wait
   curl -fsS http://127.0.0.1:8080/healthz
   ```

### Restore

The restore loads the dump into a new database, `gobb_new`, and checks it. Only then
does it swap the two: the live `gobb` becomes `gobb_old`, and `gobb_new` becomes
`gobb`. A dump that fails partway stops the restore before the swap, so the live
database keeps its data. The restore does not use `pg_restore --clean`: that option
drops only the tables that are in the dump, so a table that a newer migration added
would stay, and the next upgrade would try to create it again. A new database has no
such table. After the swap, the restore empties the data volume and unpacks the
archive.

Set the folder of the backups first. The line keeps a `BACKUP_DIR` that this shell
already has, and otherwise uses the folder of the backup section. A new shell does not
keep it, so set it again if your backups live in another folder:

```sh
export BACKUP_DIR="${BACKUP_DIR:-$HOME/gobb-backups}"
```

The first command picks the newest stamp for which both files exist and are not empty,
and prints it. Check that it is the backup you want. To restore an older backup, set
`STAMP` to its date.

<!-- block: restore-pick -->
```sh
export STAMP=$(ls "$BACKUP_DIR" | sed -n 's/^gobb-\([0-9]*-[0-9]*\)\.dump$/\1/p' | while read -r s; do [ -s "$BACKUP_DIR/gobb-$s.dump" ] && [ -s "$BACKUP_DIR/gobb-data-$s.tar.gz" ] && echo "$s"; done | tail -n 1)
echo "STAMP=$STAMP"
```

The second command is one chain, and the first failure stops it. Each step that fails
prints its own message on stderr, and the chain exits with a non-zero status. The
`postgres` service must be running. The chain does these steps in order:

1. It checks the inputs. `STAMP` is not empty, the dump and the archive both exist and
   are not empty, `pg_restore --list` reads the dump, and `tar tzf` reads the archive.
   No database called `gobb_old` exists (see the end of this section).
2. It stops gobb, drops any `gobb_new` that an earlier restore left, and creates an
   empty `gobb_new`.
3. It loads the dump into `gobb_new`. `--exit-on-error` stops at the first error, so a
   damaged or truncated dump fails here.
4. It checks that `schema_migrations` and `users` in `gobb_new` both hold rows.
5. It swaps the databases in one transaction. A rename needs no connections to either
   database, and gobb is stopped, so nothing holds one.
6. It empties the data volume and unpacks the archive.

<!-- block: restore -->
```sh
{ test -n "$STAMP" || { echo "STAMP is empty: no backup in $BACKUP_DIR has both a dump and an archive" >&2; false; }; } && \
{ test -s "$BACKUP_DIR/gobb-$STAMP.dump" || { echo "no dump for $STAMP in $BACKUP_DIR" >&2; false; }; } && \
{ test -s "$BACKUP_DIR/gobb-data-$STAMP.tar.gz" || { echo "no archive for $STAMP in $BACKUP_DIR" >&2; false; }; } && \
{ docker compose exec -T postgres pg_restore --list < "$BACKUP_DIR/gobb-$STAMP.dump" > /dev/null || { echo "pg_restore cannot read the dump for $STAMP" >&2; false; }; } && \
{ tar tzf "$BACKUP_DIR/gobb-data-$STAMP.tar.gz" > /dev/null || { echo "tar cannot read the archive for $STAMP" >&2; false; }; } && \
{ test "$(docker compose exec -T postgres psql -U gobb -d postgres -tA -c "select count(*) from pg_database where datname = 'gobb_old'")" = 0 || { echo "gobb_old exists from an earlier restore, or psql failed: drop gobb_old first" >&2; false; }; } && \
docker compose stop gobb && \
docker compose exec -T postgres psql -U gobb -d postgres -v ON_ERROR_STOP=1 -c 'drop database if exists gobb_new with (force)' -c 'create database gobb_new' && \
{ docker compose exec -T postgres pg_restore -U gobb -d gobb_new --exit-on-error --no-owner < "$BACKUP_DIR/gobb-$STAMP.dump" || { echo "pg_restore failed for $STAMP: the live database is untouched" >&2; false; }; } && \
{ test "$(docker compose exec -T postgres psql -U gobb -d gobb_new -tA -c 'select (select count(*) from schema_migrations) > 0 and (select count(*) from users) > 0')" = t || { echo "gobb_new for $STAMP has no migrations or no users: the live database is untouched" >&2; false; }; } && \
{ docker compose exec -T postgres psql -U gobb -d postgres -v ON_ERROR_STOP=1 -1 -c 'alter database gobb rename to gobb_old' -c 'alter database gobb_new rename to gobb' || { echo "the swap failed: the live database is untouched" >&2; false; }; } && \
docker run --rm --volumes-from "$(docker compose ps -aq gobb)" -v "$BACKUP_DIR:/backup:ro" \
  --entrypoint sh postgres:18 \
  -c 'tar tzf "/backup/gobb-data-$1.tar.gz" > /dev/null && find /var/lib/gobb -mindepth 1 -delete && tar xzf "/backup/gobb-data-$1.tar.gz" -C /var/lib/gobb' sh "$STAMP"
```

Then set `GOBB_VERSION` back to the version that wrote the backup, and run
`docker compose up -d --wait`. gobb reads the legacy routes and the stored password
hashes once at start, so always start it after a restore.

**If the chain fails before the swap**, the live `gobb` database is as it was. Read the
message and fix the cause. Gobb may be stopped, so drop the partial `gobb_new` and start
gobb again on the live database, with the `GOBB_VERSION` that you have now. To try
again, run the chain again: it drops `gobb_new` itself.

<!-- block: restore-cleanup -->
```sh
docker compose exec -T postgres psql -U gobb -d postgres -v ON_ERROR_STOP=1 -c 'drop database if exists gobb_new with (force)'
docker compose up -d --wait
```

**After the swap**, `gobb_old` holds the database as it was before the restore. Keep it
until the restored board works: sign in and open a few pages. Then drop it. The next
restore refuses to start while `gobb_old` exists, so that it cannot overwrite a copy
you still want:

<!-- block: restore-drop-old -->
```sh
docker compose exec -T postgres psql -U gobb -d postgres -v ON_ERROR_STOP=1 -c 'drop database gobb_old'
```

CI runs these commands on every push. It restores a dump over a database that holds an
extra table, and checks that the table is gone and that the row count of every table is
the same (`scripts/rowcounts`). It runs the restore with the archive missing, with an
unreadable dump and with an unreadable archive, and checks that the restore deletes
nothing and fails. It runs the restore with a truncated dump and with the dump of an
empty database, and checks that the restore fails before the swap, that the live
database keeps every row, and that gobb serves its pages again on it.

## Run the server

`gobb serve` needs a PostgreSQL database and a data directory. It applies the pending
migrations at start, so a new database needs no `gobb migrate` first. A new database
holds no board: `gobb import` loads one (see "Import a bundle"). Until then every page
answers 503 and says to run `gobb import`, and `/healthz` answers 200. The server reads
the legacy route table once at start, so restart it after an import.

Every error page, 5xx included, is a page of the board in its default scheme. The server
keeps the board name, the header image and the default scheme in memory, so the error
page needs no database: when the database is down or slow, a page answers 500 as a
guest's page, with no session and no counts. When a template fails, the answer is a 500
or 503 page that the server built at start. A `Range` header that no byte of a file fits
answers 416 in the same layout, and so does a path under the base path that spells a
letter of the base path as a percent escape (404). Only `/healthz` answers in plain text.

For crawlers, `/robots.txt` answers at the host root, whatever the base path, and the
sitemap at `sitemap.xml` under the base path. Both show the guest's view: the sitemap
lists the index, each forum and topic page that a guest can open, and nothing that only
members see. `robots.txt` keeps every crawler out of the search pages and names the
sitemap. The admin settings page picks which AI crawlers it lets in: all (the default),
search and answers only, or none. The sitemap is built at most once in 15 minutes.
Set `GOBB_PUBLIC_URL`: the canonical links need it, and it makes the addresses in both
files name the public host.

The server reads these variables. Docker Compose reads the last one:

| Variable | Meaning | Default |
|---|---|---|
| `GOBB_DSN` | PostgreSQL connection string. Required. | none |
| `GOBB_LISTEN` | Address to bind. | `127.0.0.1:8080` |
| `GOBB_DATA_DIR` | Directory for avatars, smilies, ranks, the header image and attachments. | `/var/lib/gobb` |
| `GOBB_BASE_PATH` | URL path prefix, such as `/forum/`. | `/` |
| `GOBB_TRUSTED_PROXIES` | Comma-separated CIDRs of the proxies to trust. | `127.0.0.1/32,::1/128` |
| `GOBB_COOKIE_SECURE` | `true` or `false`. Mark the cookies Secure. | `true` |
| `GOBB_PUBLIC_URL` | The address of the board, such as `https://board.example.org/forum/`. The links in email, `/robots.txt`, the sitemap and the canonical links of the pages use it. The rules are in "Email". Unset, the pages have no canonical link, the sitemap and `robots.txt` take the host of each request, and the server logs a warning at start. | unset |
| `GOBB_SMTP_HOST`, `GOBB_SMTP_PORT`, `GOBB_SMTP_TLS`, `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD`, `GOBB_MAIL_FROM`, `GOBB_MAIL_PER_HOUR`, `GOBB_MAIL_PER_DAY` | Email settings (see "Email"). | email off |
| `GOBB_METRICS_LISTEN` | Address of the metrics listener, such as `127.0.0.1:9187` (see "Metrics"). | off |
| `GOBB_DB_MAX_CONNS` | The most database connections that the server opens, from 1 to 100. It replaces any `pool_max_conns` in `GOBB_DSN` (see "Load limits"). | `4` |
| `GOBB_DB_POOL_WAIT` | The longest time that a request waits for free database connections, in total over the request, as a duration from `10ms` to `5s`. | `1s` |
| `GOBB_SEARCH_PER_SECOND` | The most searches that the whole board runs in any second, from 1 to 1,000. | `5` |
| `GOBB_SEARCH_MAX_RUNNING` | The most searches that run at one time, from 1 to `GOBB_DB_MAX_CONNS` − 1, so searches never hold the whole pool (a pool of one allows 1). | `2` (`1` when `GOBB_DB_MAX_CONNS` is 1 or 2) |
| `GOBB_PAGE_CACHE_MIB` | The memory of the guest page cache, in MiB, from 0 to 1,024. `0` turns the cache off. The server uses at most a quarter of the Go memory limit, or 64 MiB with no limit (see "Guest page cache"). | `16` |
| `GOBB_PAGE_CACHE_WARM` | `true` or `false`. Render the guest pages into the cache at start, and again after a change drops them. | `true` |
| `GOBB_WRITE_FLUSH_INTERVAL` | The time between two writes of the batched view counts, visit times and member addresses, from `1s` to `10m` (see "Batched writes"). | `30s` |
| `GOBB_SIGNIN_GATE_WAIT` | How long after the start of the request a sign-in may still wait for room in the memory gate of the password checks, from `100ms` to `10s`. A longer wait gets 503. Despite the name, avatar and icon uploads also wait at most this long for the same gate (see "Load limits"). | `1s` |
| `GOBB_MAX_IN_FLIGHT` | The most requests under way at one time, from 1 to 100,000. A request past it gets 503 at once. Outside it: `GET /healthz`, `GET`/`HEAD` of `/favicon.ico` and the known static files, cached guest pages, and the password POSTs, which have their own limit (see "Load limits"). | `256` |
| `GOBB_MAX_SIGNIN_IN_FLIGHT` | The most password POSTs under way at one time (sign-in, password and email change, admin confirm, reset link), on slots of their own outside `GOBB_MAX_IN_FLIGHT`, from 1 to `GOBB_MAX_IN_FLIGHT`. A POST takes its slot once its body is read; one past the limit gets 503 at once. | `32` (less when `GOBB_MAX_IN_FLIGHT` is lower) |
| `GOBB_SIGNIN_FREE_FAILURES` | How many failed sign-ins of an address or a name leave no wait after them, from 0 to 100 (see "Sign-in limits"). | `2` |
| `GOBB_SIGNIN_WAITS` | The waits after the failures that follow the free ones, in order: 1 to 20 durations from `1s` to `24h`, separated by commas. | `15s,15s,60s,60s,60s` |
| `GOBB_SIGNIN_LAST_WAIT` | The wait after every later failure, from `1s` to `24h`. | `900s` |
| `GOBB_SIGNIN_RESET_AFTER` | The time with no failure that clears a count, from `1m` to `720h`, and at least the longest wait. | `24h` |
| `GOBB_ADDRESS_KEEP_DAYS` | How many days gobb keeps a member's address after its last use (a sign-in, or a signed-in visit), from 1 to 3,650. An imported address counts from the import. | `365` |
| `GOBB_DEFENSIVE_FAILURES` | Defensive Mode turns on above this many failed sign-ins of the whole board in `GOBB_DEFENSIVE_WINDOW`, from 1 to 10,000. | `100` |
| `GOBB_DEFENSIVE_WINDOW` | The window of that count, from `1s` to `1h`. | `60s` |
| `GOBB_DEFENSIVE_QUIET` | How long the rate stays at or below the trigger before Defensive Mode turns off, from `1s` to `24h`. | `10m` |
| `GOBB_SIGNIN_TABLE_KEYS` | The most counts in each of the two tables of the sign-in schedule (addresses and names), from 1,000 to 1,000,000. A full table is about 145 bytes per entry. | `50000` |
| `GOBB_DEFENSIVE_GAP` | The least time between two tries of an untrusted address while Defensive Mode is on, from `0s` (none) to `1h`. | `5s` |
| `GOMEMLIMIT` | The Go memory limit, such as `180MiB`. Unset, gobb sets 70% of the cgroup memory limit (see "Memory"). | unset |
| `GOBB_MEM_LIMIT` | Compose only: the memory cap of the gobb container, with no swap on top. | `256m` |
| `GOBB_METRICS_PORT` | Compose only, with `compose.metrics.yaml`: the host port of the metrics listener, on 127.0.0.1. | `9187` |

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

### The build line

The footer of every page ends with one line in small type. A 2xx answer to a GET or HEAD
request shows `Rendered in 0.02 s · gobb 0.1.0 · build 0f0f0fac`. An admin (the
board-wide admin right) also sees the count of database statements:
`Rendered in 0.02 s with 5 queries · gobb 0.1.0 · build 0f0f0fac`. The count is exact,
so only an admin sees it: on the page of a failed sign-in it would tell a stranger
whether a username exists. Every answer to a POST, and every answer that is not 2xx (an
error, or a page that redirects), shows `gobb 0.1.0 · build 0f0f0fac`, so the 404 of a
hidden topic looks the same as the 404 of a missing one. The time runs from the moment
the request enters the server until just before the last run of the page template,
rounded up to the next 10 ms. The line holds no member name, host name, address, memory
figure or timing of a query. Guests and members see the time, and an admin also sees
the query count. Every POST answer and every error page shows only the version and the
build.

The build is the first 8 hex characters of the commit. The image build takes it from the
`GOBB_COMMIT` build argument, and CI and the release workflow pass the commit SHA. To
set it for a local build or for `docker compose build`:

```sh
export GOBB_COMMIT=$(git rev-parse HEAD)
go build -ldflags "-X github.com/evanwtf/gobb/internal/version.Commit=$GOBB_COMMIT" -o gobb ./cmd/gobb
docker compose build   # compose.yaml passes GOBB_COMMIT on
```

With no value, gobb uses the revision that the Go tool stamps into a binary built inside
a git checkout. With neither, the line says `build unknown`. A value that is not 7 to 40
hex characters also shows as `unknown`.

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

### Email

gobb sends email through one SMTP server. Email is off until `GOBB_SMTP_HOST` is set.
Each variable below is read from the environment. An empty value counts as unset.
Docker Compose passes all of them through from `.env`.

| Variable | Meaning | Default |
|---|---|---|
| `GOBB_SMTP_HOST` | The SMTP server. Turns email on. With no host, any other variable in this table but `GOBB_PUBLIC_URL` is an error. | unset |
| `GOBB_SMTP_PORT` | The port, 1 to 65535. | `587` for `starttls`, `465` for `implicit` |
| `GOBB_SMTP_TLS` | `starttls` or `implicit`. There is no plain mode. | `starttls` |
| `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD` | Both or neither. gobb signs in with AUTH PLAIN, and only after TLS runs. | unset |
| `GOBB_MAIL_FROM` | The sender: one plain address, with no display name. Required with a host. | none |
| `GOBB_PUBLIC_URL` | The address of the board, for links in email and for the crawler pages. It starts with `https://`, names a host, holds no user information, query or fragment, and its path equals `GOBB_BASE_PATH`. It starts with `http://` only when `GOBB_COOKIE_SECURE=false`. gobb stores it with a trailing `/`. Required with a host; allowed without one. | none |
| `GOBB_MAIL_PER_HOUR`, `GOBB_MAIL_PER_DAY` | The most reset emails the whole board sends in any hour and in any 24 hours, from 1 to 100,000. The hourly limit must not be more than the daily one. To change them, edit `.env` and run `docker compose up -d`. | `10`, `20` |

gobb talks to the server over TLS only. With `starttls`, it sends no AUTH and no
message to a server that does not offer STARTTLS. It checks the certificate of the
server against the system roots and needs TLS 1.2 or later. A setting that is wrong
stops the command or the server at start, with an error that names the variable. No
error and no log line holds the password.

Example values, for a board at `https://board.example.org/forums/`:

```sh
GOBB_SMTP_HOST=smtp.example.org
GOBB_SMTP_PORT=587
GOBB_SMTP_TLS=starttls
GOBB_SMTP_USER=board@example.org
GOBB_MAIL_FROM=board@example.org
GOBB_PUBLIC_URL=https://board.example.org/forums/
GOBB_BASE_PATH=/forums/
```

Put `GOBB_SMTP_PASSWORD` in `.env` and nowhere else: not in `compose.yaml`, not in a
shell history, not in an issue. Keep `.env` out of git. Then send a test email. The
command sends at once, through the settings above, and prints nothing on stdout. A
success logs `test email sent` on stderr. A failure exits 1 and logs a short reason
(`smtp 550 5.1.1`, `timeout`, `tls`, `no starttls` or `network`) with no text of the
server's reply and no address:

```sh
export ADDRESS=you@example.org
docker compose exec -T gobb gobb mail test "$ADDRESS"
```

Without Docker, run `./gobb mail test "$ADDRESS"` with the same variables set. The
command needs no `GOBB_DSN`. The exit code is 0 when the server accepted the message,
1 on a missing setting or a failed send, and 2 on a wrong call.

If the SMTP password holds `$`, `#` or a double quote, wrap it in single quotes in
`.env`: `GOBB_SMTP_PASSWORD='pa$s#word'`. Compose expands `$`, and treats ` #` as the
start of a comment. A password with a single quote cannot go in single quotes: make a
new password without one.

To see why a send failed, read the log. The worker logs a WARN line `email failed`
(with the row `id`, `user_id`, `attempt` and `reason`) for each failed try, and
`email dropped` when it gives up on a message. Neither line holds an address:

```sh
docker compose logs gobb | grep -E 'email (failed|dropped)'
```

The column `email_queue.last_error` holds the last reason of each row.

Two places hold the plain text of a live reset link. The table `password_resets` holds
only a hash:

- **The queue.** A queued reset email holds its link until the worker sends it: a few
  seconds normally, and up to 1 hour during an SMTP outage. A database dump taken in
  that window holds the link too.
- **A reverse proxy's access log.** The proxy records each `/reset/` request, token
  included. A link works once and for 1 hour, but logs last longer. Configure the proxy
  not to log paths that start with `/reset/` (under `GOBB_BASE_PATH`, if it is set).

### Reset a password, ban or deactivate a member

A member resets the password by email. With email on (see "Email" above), the
sign-in page shows "Forgot your password?". That link opens a form for the username
or the email address, and gobb emails a second link to the member. The emailed
link works once, for 1 hour. Opening it changes nothing; saving a new password
ends every session of the member. With email off, the links and the pages do not
exist.

gobb sends at most 3 reset emails a day to one account, and on the whole board at
most `GOBB_MAIL_PER_HOUR` an hour and `GOBB_MAIL_PER_DAY` a day (10 and 20 unless
set). Past any of these limits, a request gets the usual answer and no email. A full
board limit logs the WARN line `reset email skipped: board limit`, with `per=hour`
or `per=day`. One client address may ask 5 times an hour.

An administrator (a member with the board-wide `admin` right) gets no reset email,
and an emailed link to an account that becomes an administrator stops working. An
old imported address must not be a way to take an administrator account. An
administrator resets the password from the command line, as below.

The administrator resets a password from the command line. Run the commands where
the database is. With Docker Compose, run them in the running container. The `-T` flag keeps Compose from adding a
terminal, which would change the line endings:

```sh
export NAME=alice
docker compose exec -T gobb gobb user reset-password "$NAME"
docker compose exec -T gobb gobb user ban "$NAME" -reason "spam run"
docker compose exec -T gobb gobb user unban "$NAME"
docker compose exec -T gobb gobb user activate "$NAME"
docker compose exec -T gobb gobb user deactivate "$NAME"
docker compose exec -T gobb gobb ban list
export ID=2
docker compose exec -T gobb gobb ban lift "$ID"
```

Without Docker, run `./gobb user ...` or `./gobb ban ...` with `GOBB_DSN` set, as for `gobb import`.

`<name>` is the username. Case and spacing do not matter, as at sign-in. A name that
no member has exits 1 with `no member named <name>` on stderr, and changes nothing.

- **`reset-password`** gives the member a new random password of 16 letters and
  digits, with no look-alike characters (no `0`, `O`, `1`, `l`, `I`). It prints the
  password once, as the only line on stdout. It ends every session of the member. Give
  the password to the member by a private route, and tell the member to change it on
  the account page. The password is never an argument, so it is not in the shell
  history. It is in no log and in no row of the moderation log. To keep it out of the
  scrollback too, capture it: `PW=$(docker compose exec -T gobb gobb user reset-password "$NAME")`.
- **`ban`** adds a ban with no end date, ends every session of the member and removes
  the addresses that the member used (the trusted addresses), so an unban does not
  bring that trust back. The member cannot sign in. `-reason` is for staff: the banned member does not see it.
  The flag may come before or after the name. A member who is banned already gets no
  second ban: the command exits 0 and says so on stderr.
- **`unban`** removes the bans on the member that have not ended. A member with no
  such ban: the command exits 0 and says so on stderr.
- **`activate`** makes an inactive account active. The member can then sign in with
  the old password, or ask for a reset email. An account can arrive inactive from an
  imported board. The bans stay: `activate` is not `unban`. An active account: the
  command exits 0 and says so on stderr.
- **`deactivate`** makes the account inactive and ends every session of the member.
  An inactive member cannot sign in and gets no reset email, and a reset link sent
  earlier stops working. The member list leaves the member out. The posts, the
  password and the email address stay, so `activate` gives the same sign-in back. An
  inactive account: the command exits 0 and says so on stderr.
- **`ban list`** prints the bans in force of every kind (user, email and IP) to
  stdout, newest first, as a table: `ID`, `KIND`, `TARGET` and `ENDS`. `ENDS` is the
  end in UTC, or `never`. With no bans in force, it prints nothing to stdout and says
  so on stderr. Use it to find the ID for `ban lift`.
- **`ban lift <id>`** removes one ban of any kind, as the Bans page does. Use it when
  an email ban or an IP-range ban locks out every administrator. An ID with no ban in
  force (no such ban, a ban that has ended, or one lifted already) exits 1 with
  `no ban in force with ID <id>` on stderr, and changes nothing. It writes the
  moderation log row `ban.lift` with no actor, and the kind, the target and
  `"via": "cli"` as details.

Each command that changes something writes one row to the moderation log (`user.reset_password`,
`user.ban`, `user.unban`, `user.activate`, `user.deactivate`) with no actor and `{"via":"cli"}` as details. Messages go to
stderr. The exit code is 0 on success, 1 on an unknown name or a failure, and 2 on a
wrong call. A failed command changes nothing, with one exception: if `reset-password`
cannot print the new password (the write to stdout fails), the reset has committed and
the old password no longer works. The command exits 1 and says so on stderr. Run it
again to get a new password. On success, every command logs the stored username and
the ID of the member it matched on stderr, so a name that the fold matched to another
member shows at once.

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

Failed sign-ins follow a schedule. gobb keeps one count of failures for each client
address (a client on IPv6 counts as its whole /64) and one for each typed name. The
first two failures of a count are free. After that, the next try must wait 15 s,
15 s, 60 s, 60 s and 60 s, then 900 s before every later try. A try made before its
wait ends gets 429 with `Retry-After` and "Too many sign-in attempts. Try again in N
seconds." on the sign-in page. It runs no password check, and the count does not
advance. The wait it reports is the larger of the waits of the address and of the
name.

For example, with the defaults: a stranger who guesses at one name from one address
makes three tries at once, all wrong. The fourth try waits 15 s, the fifth 15 s more,
then 60 s three times, then 900 s each time. With `GOBB_SIGNIN_FREE_FAILURES=1`, the
third try waits 15 s instead.

Notes for the operator:

- A good sign-in clears the count of the name. It does not clear the count of the
  address, so one account of one's own cannot reset an address that guesses at other
  names. A count clears after 24 hours with no failure (`GOBB_SIGNIN_RESET_AFTER`,
  which must be at least the longest wait). A completed password reset clears the
  counts of the member's username and email address, so a member whom a stranger
  holds in a wait can get back in through a reset.
- The name count counts the name as typed, after cleaning. A username and an email
  address for one account are two names, so that account has two counts.
- A member's *trusted addresses* are the addresses that the member signed in from or
  visited from while signed in, kept for one year after the last use of each
  (`GOBB_ADDRESS_KEEP_DAYS`). A visit's address reaches the database with the batched
  writes (see "Batched writes"). An
  address is trusted only while its member is active and not banned. A ban and the
  Spammer action remove the member's addresses. A failure from one of the member's
  trusted addresses does not count against the name, and the wait of the name does
  not hold there. The count of the address still applies. At a locked name, one
  database query decides whether the address is trusted, and costs the same whether
  or not the name has an account. The answer stays in memory while the wait lasts,
  so later refused tries cost no database work.
- **Defensive Mode** turns on when the whole board counts more than 100 failed
  sign-ins in 60 s (`GOBB_DEFENSIVE_FAILURES`, `GOBB_DEFENSIVE_WINDOW`). It turns
  off 10 minutes after the rate last stood above that (`GOBB_DEFENSIVE_QUIET`),
  however many failures still trickle in below it. While it is on,
  an address that is no member's trusted address has no free failure, and waits at
  least 5 s between two tries (`GOBB_DEFENSIVE_GAP`); a trusted address keeps the
  normal schedule. Guests cannot search: they get the page of a search over the
  limit, with `Retry-After: 60`. gobb writes one WARN line `defensive mode on` and one
  INFO line `defensive mode off`, with counts and no address.
- The counts and Defensive Mode live in memory. A restart sets them to zero. Each of
  the two tables of counts (addresses, names) holds at most 50,000 entries
  (`GOBB_SIGNIN_TABLE_KEYS`), about 7 MiB when full. A full table first drops the
  counts that hold nothing; when that frees less than an eighth, it evicts down to
  seven eighths full: first the counts not in a wait, then the counts with the
  fewest failures, then the oldest. So a spray of new names does not flush a count
  that holds a wait. `gobb_signin_schedule_evictions_total` counts the evictions.
- Behind a reverse proxy, `GOBB_TRUSTED_PROXIES` must name the proxy. Without it,
  every visitor has the address of the proxy and shares one count. The same mistake
  makes the proxy's address a trusted address for every member who signs in.
- The start-up log line `sign-in schedule` gives the values of the settings.
- A failed sign-in takes at least the floor, even when the client closes its end of
  the connection early. This holds for every failure after the schedule lets the try in: a wrong
  password, a busy server, an inactive account and a ban. The floor counts from the
  start of the sign-in, moved forward by the time that it waits for the memory gate.
  So a wait for the gate adds the same delay to every name, and the floor still
  hides the lookup and the hash after it. The server sets the floor at start:
  2 × K × T, but at least 400 ms, rounded up to a whole 50 ms. A hash shape is the
  format of a stored hash and its cost parameters. T is the time of a password
  check for the slowest hash shape. The server times five checks of each hash
  shape, in rounds of one check per hash shape, and keeps the lowest time of each.
  Each check uses a wrong password of 4,096 bytes (the longest that a check reads;
  a chain check takes longer for a longer password). K is the most accounts that
  share one email address, because a sign-in by that address checks each of them.
  The start-up log line `login: sign-in floor` gives `floor_ms` (the floor),
  `t_ms` (T), `k` (K), `shapes` (the number of hash shapes) and `samples` (the
  checks per hash shape). The floor hides the time of the hash while the host runs
  a check within twice that lowest time. Two checks at once on one CPU already take
  about twice as long, so heavier CPU load can still show the time. If `floor_ms`
  is high after a start on a busy host, restart the server when the host is quiet.
- A sign-in holds the memory gate once, for all of its checks. So a busy server
  answers alike for a shared email address, a unique one and an unknown name.
- Password checks share a memory gate of 128 MiB, or less under a low memory limit
  (see "Memory"). Every check holds the same share,
  whatever the hash shape, so neither the wait nor the answer tells a stranger
  which names have accounts or which accounts have old hashes. The share follows the
  most costly stored hash, which the server reads at start. With no hash above the
  cost of a new gobb hash (19 MiB), six checks run at once. On a board with imported
  64 MiB argon2id hashes, two checks run at once. The start-up log line
  `login: password check charge` gives the share in KiB and the number of checks at
  once. A sign-in replaces an old hash with a gobb hash, so the share falls at a
  later start, when no costly hash is left. A check that finds no room for 5
  seconds answers 503.

### Load limits

gobb never lets a request wait long for a database connection. A flood of visitors
then gets quick refusals, and the server stays up.

- The pool holds at most `GOBB_DB_MAX_CONNS` connections (4 unless set). The email
  worker, the cleanup, and the queue and session gauges of the metrics (one query
  each, at most every 15 s) share the pool with the requests. The pool gauges read
  the pool's own counts and take no connection.
- A request waits at most `GOBB_DB_POOL_WAIT` (1 s unless set) for connections,
  in total: a page that takes several connections in turn shares one budget, so
  its 503 comes within it. The limit covers the waits only, and a query that has
  its connection keeps the 10 s deadline of the request. A request that waits longer gets 503 with
  `Retry-After: 2`, and later waits of that request fail at once. The 503 page
  comes from memory and needs no connection. `gobb_db_pool_wait_timeouts_total`
  counts these requests, and a WARN line gives their count at most once a minute.
- Search has its own limits, for the whole board: `GOBB_SEARCH_PER_SECOND`
  searches in any second (5 unless set), and `GOBB_SEARCH_MAX_RUNNING` searches at
  one time (2 unless set, and never the whole pool). There is no limit per address
  or per member. A search over a limit gets 429 with `Retry-After`, on a page of
  the board that says "Search functionality is currently disabled." That page
  touches no database.
  Only a request with search words or an author counts; the bare search form does
  not. `gobb_rate_limited_total{limit="search"}` and `gobb_search_busy_total`
  count the refusals.
- The pool wait shows only when queries are slow: a large board, a cold disk, or
  a long search. On the small example board each query takes about a millisecond,
  and the load test of CI runs out of CPU before any request waits 1 s for a
  connection. So that test sees no 503 from the pool, and does not try to force one.
- A sign-in that finds the memory gate of the password checks full (see "Memory")
  waits for room, then gets 503 with `Retry-After: 5`. The wait ends
  `GOBB_SIGNIN_GATE_WAIT` (1 s unless set) after the start of the request, so a
  sign-in that already spent that long waiting for the pool does not wait for the
  gate as well. A busy sign-in then answers within about 1 s of waits plus the
  floor of a failed sign-in. `gobb_signin_attempts_total{result="busy"}` counts
  these. Avatar and icon uploads wait for the same gate, each for at most
  `GOBB_SIGNIN_GATE_WAIT`, despite the name.
- The server handles at most `GOBB_MAX_IN_FLIGHT` requests at one time (256
  unless set). A request past the limit gets 503 with `Retry-After: 2` at once, on
  the busy page from memory; it does not wait in a queue.
  `gobb_in_flight_refusals_total{limit="all"}` counts the refusals. These skip the
  limit:
  - `GET /healthz`, which pings the database at most once a second, and
    `GET` or `HEAD` of `/favicon.ico` and of the known files under `/static/`:
    they are served from memory, and the busy page links the stylesheet. Another
    method, or an unknown static name (its 404 page runs queries), takes a slot.
  - a guest page that the page cache holds: a `GET` or `HEAD` with no session
    cookie whose page is in the cache is served from memory before the limit,
    with no render and no database (the view of a topic goes to the batched
    writes). A miss, any request with a session cookie (even a forged or ended
    one, which needs a lookup), a path with escapes, and any other method take a
    slot. These hits count in `gobb_page_cache_hits_total` with the others.
    Without this, requests that wait for a database connection held every slot
    under load, and cached pages got 503.
  - the password POSTs, which have slots of their own (below).
- A request holds its slot for as long as it lasts, a slow request body and a
  slow reader of the answer included. Put a reverse proxy in front that buffers
  request and response bodies (nginx does by default), so that slow clients wait
  on the proxy and not on gobb. Without one, slow clients can hold every slot.
- The password POSTs are `POST /login`, `/account/password`, `/account/email`
  (both check the current password), `/admin/confirm` (it checks the admin's
  password) and `/reset/{token}` (it hashes the new password). They do not count
  in `GOBB_MAX_IN_FLIGHT`. Instead, each one takes a slot of
  `GOBB_MAX_SIGNIN_IN_FLIGHT` (32 unless set) once the server has read its body
  (at most 48 KB), and keeps it to the end of the request. A slow body holds no
  slot while it comes, so slow clients cannot lock out every sign-in. A wrong
  password holds its slot for the floor of a failed sign-in and the gate wait, so
  without these slots a flood of sign-ins would fill the main limit and refuse the
  pages. One past the limit gets the same 503 at once;
  `gobb_in_flight_refusals_total{limit="signin"}` counts it. The bodies read
  before a slot cost memory, at most 48 KB each; a sign-in form is a few hundred
  bytes.
- The limits bound the work under way, not the open connections: each open
  connection costs about 27 KB whether its request runs or not. Under the 256 MiB
  cap, the load test at 4,096 connections at once (measured 2026-11-08 at commit
  43ae783, client and server on one Linux VM) peaked at 204 and 207 MiB of the
  cgroup in two runs, with no OOM kill, and every guest page view got 200. Before
  the in-flight limits, the same load killed gobb. A limit from 64 to 512 changed
  the peak by less than 10 MiB (measured earlier, before the bypass of the page
  cache). At 4,096 the CPU is saturated, and the slowest refusal took 2.1 to 2.3 s;
  at the 512 of CI it took 0.3 s.
- The start-up log line `load limits` gives these values, and the three of the
  page cache and the batched writes below.

### Guest page cache

A page that a guest can see is built once and served from memory: the index, each
page of a forum or a topic, and the static pages. A member always gets a live page.
A flood of guests then costs the database almost nothing.

- The cache holds at most `GOBB_PAGE_CACHE_MIB` of pages (16 MiB unless set). Over
  the limit, the least recently used page goes. The first board's guests see about
  160 pages of at most 75 KB, so the default holds every one. `0` turns the cache off.
  The server caps the setting at a quarter of the Go memory limit (a quarter of 70%
  of `GOBB_MEM_LIMIT` under Compose: 44 MiB at `256m`), or at 64 MiB when there is no
  limit, and logs a warning when it cuts the setting. The count of an entry covers its
  body and its bookkeeping, and is close to the real use of the heap.
- A page is kept by its path, its page number and the color scheme. No scheme and the
  board's default scheme share a page. The note of a held post (`?held=1`) is filled
  in for each request. Any other query parameter is left out of the key and of the
  page, so a random query string cannot force a render. The page never holds the host
  of a request.
- Each request fills in its own csrf token and the time in the build line. A cached
  page carries a weak `ETag`, and a request that sends it back in `If-None-Match` gets
  304. `Cache-Control` stays `private, no-cache`.
- When many guests ask for a missing page at once, one request renders it and the
  others wait for that render. Only a page of status 200 is kept: an error page or a
  redirect is rendered for each request.
- A write drops the pages it changes. A reply, an edit, a delete, a restore, an
  approval, a lock or a change of the topic type drops the pages of that topic, the
  index, every forum page, and the pages that show the author's posts. A new topic
  drops the index, the forum pages and the pages that show the author's posts. A new
  post that waits for approval drops nothing: a guest does not see it. A change to a
  signature, an avatar or a profile drops the pages that show that member's posts; a
  birthday drops the index too.
- Every write in the admin area drops every page, refused or not. Outside the admin
  area, a write that the board refuses (bad input, no right, a locked topic, no such
  post) drops nothing. A write that fails for any other reason (a timeout, a lost
  connection) drops its pages at once, and once more 2 s later, since it may still
  commit.
- The commands that write from outside the server (`gobb user`, `gobb ban lift`,
  `gobb import`) set a mark in the database. The server reads it every 5 s and empties
  the cache when it changes.
- A change made with SQL by hand reaches no cache. Restart the server after it, or
  bump the mark: `update page_cache_epoch set value = value + 1`.
- The index lists today's birthdays, so it ends at midnight in the board's time zone.
  A topic page shows the view count of its render until a change drops it. The
  download counts of its attachments are those of the render too; the server does not
  change them, so only a change by hand makes them old.
- At start, unless `GOBB_PAGE_CACHE_WARM` is `false`, a worker renders pages until the
  cache is full: the index, the first page of each forum, then every page of each
  topic, newest activity first. It renders one page at a time, and waits while all
  but one of the pool's connections are in use. After a write drops pages, it renders
  them again.

### Batched writes

A page view does not write. View counts, the activity times of members and the
addresses of their visits wait in memory, and go to the database in one transaction
every `GOBB_WRITE_FLUSH_INTERVAL` (30 s unless set). The server writes them once more
when it stops, inside the 30 s `stop_grace_period`. A crash loses at most one interval.

- The first page of a member's new visit (more than 30 minutes since the last
  activity) writes the visit at once, because "New since your last visit" on that page
  reads it.
- Each signed-in visit records the member's address, which keeps a "remember me"
  member's current address trusted (see "Sign-in limits"). A member who is banned or
  inactive by the time of the write gets no address.
- A failed write is logged, and its rows wait for the next one.

### Memory

At start, gobb sets the memory limit of the Go runtime. When `GOMEMLIMIT` is set,
that value wins. Otherwise, when the process runs under a cgroup memory limit (a
container cap, such as `mem_limit` in Compose), gobb sets the limit to 70% of the cap.
Otherwise it sets none. Without a limit, the Go runtime lets the heap grow to about
twice the memory in use before it collects. Under a container cap, a burst of failed
sign-ins then passes the cap, the kernel kills gobb, and the restart clears the
sign-in limits.

The password checks share a memory gate of min(128 MiB, the limit − 48 MiB). The
start-up log line `memory limit` gives the limit in bytes, its source (`GOMEMLIMIT`,
`cgroup` or `none`) and the gate budget in KiB. A check gives its share of the gate
back only after a garbage collection. A sign-in that holds the gate for several
checks (a shared email address) also collects before each check after the first. So
the memory of a finished check is free before the next check starts. At start, gobb
also times five checks of each hash shape, one at a time inside the gate, and then
frees the memory of those checks.

Compose runs gobb under a cap of 256 MiB with no swap. `GOBB_MEM_LIMIT` in `.env`
changes it. Under that cap the Go limit is 179 MiB and the gate is 128 MiB, so two
checks of a 64 MiB argon2id hash run at once. CI sends a burst of 40 wrong passwords,
20 at a time, against such hashes under this cap, and fails on any restart or kill.

A stored argon2id hash may ask for up to 256 MiB. A hash that asks for more than the
gate budget gets one WARN line at start, with the number of such accounts. A check of
such a hash runs alone, but it still needs its own memory and the rest of the server's.
For such a board, raise `GOBB_MEM_LIMIT` to at least (m + 48 MiB) / 0.7, where m is the
largest memory cost of a stored hash: `512m` covers m = 256 MiB. Above m = 128 MiB the
WARN line stays, because the gate never holds more than 128 MiB.

Press Ctrl-C or send SIGTERM to stop. The server finishes the open requests and closes
the database pool, inside 20 seconds. A second signal exits at once.

### Load test

`scripts/loadtest` sends a mix of requests to a running gobb for a set time, then
checks that the server refused quickly and recovered. CI runs it on the example
bundle under the 256 MiB cap, after the burst of wrong passwords. To run it on a
local stack, start the stack as CI does: the CI files trust every peer as a proxy
(so the test can send each request from its own address) and allow sign-in over
plain HTTP. Use these files on a test stack only.

```sh
export COMPOSE_FILE=compose.yaml:compose.metrics.yaml:.github/compose.ci.yaml:.github/compose.memcap.yaml
export POSTGRES_PASSWORD=test
docker compose up -d --build --wait
docker compose stop gobb
docker compose run --rm -v "$PWD/docs/example-bundle:/import:ro" gobb import /import
docker compose up -d --wait
export BASE="http://$(docker compose port gobb 8080)/"
go run ./scripts/loadtest -duration 60s -c 256 -search-forbidden \
  -member-name alice -member-password example-password "$BASE"
sh scripts/memcap-check.sh "$(docker compose ps -q gobb)" 268435456
```

The mix holds guest views of the index, the forums and the topics; guest views of
pages that do not exist (a topic past the last one, a page number past the end);
guest searches; wrong passwords over 500 names and 5,000 IPv6 /64s; and the pages
and searches of a signed-in member. `-mix` sets the weight of each class, and
`-help` lists every flag. The example board does not let guests search, so
`-search-forbidden` allows a 403 for a guest search.

The test fails when:

- an answer is not 200 or 304; or 404 for a page that does not exist; or 429 or
  503 for any class but a guest page view (the page cache serves those before
  the limits, so a refused one means the cache stopped doing so);
- a 429 or a 503 takes longer than 2 s (`-refuse-bound`), or has no
  `Retry-After`;
- a request gets no answer: a dropped connection, nothing within 30 s, or a
  request that the end of the load cut off after it had run longer than 2 s;
- after the load and a 5 s pause, a page of the set-up, `/login` (a guest page
  that the cache does not hold) or `/healthz` does not answer 200 within 2 s
  (`-recovery-bound`).

It prints, for each class, the count of each status, the 50th, 95th and 99th
percentiles of the time, the slowest request and the slowest refusal.
`scripts/memcap-check.sh` then reads the cgroup of the container (Linux only): it fails on a restart, an OOM kill,
or a peak at the cap. The test sends requests only to a loopback address, unless
you pass `-any-host`.

### Metrics

gobb serves Prometheus metrics on a second listener. It is off until
`GOBB_METRICS_LISTEN` is set, for example to `127.0.0.1:9187`. That listener serves only
`GET /metrics`, in the Prometheus text format. It is outside the base path and outside
the main listener, so a reverse proxy in front of the board does not show it.

The endpoint has no auth. It holds no member data, but it shows the shape of the
traffic. Bind it to a loopback or a private address. A public address logs a WARN
line at start.

With Docker Compose, add `compose.metrics.yaml` to `COMPOSE_FILE` in `.env`:

```sh
COMPOSE_FILE=compose.yaml:compose.metrics.yaml
```

It sets `GOBB_METRICS_LISTEN=0.0.0.0:9187`, the address inside the container, and
publishes the port on the host's `127.0.0.1` only, at `GOBB_METRICS_PORT` (`9187`
unless set). The start-up line warns about every interface; in a container that is
the usual address. With `COMPOSE_FILE` set, Compose reads only the files it names, so
name `compose.override.yaml` there too if you have one. Then:

```sh
curl -fsS http://127.0.0.1:9187/metrics | grep '^gobb_'
```

| Metric | Type | Labels |
|---|---|---|
| `gobb_http_requests_total` | counter | `route`, `method`, `code` |
| `gobb_http_request_duration_seconds` | histogram, 5 ms to 10 s | `route` |
| `gobb_signin_attempts_total` | counter | `result`: `ok`, `wrong`, `ambiguous`, `limited`, `banned`, `inactive`, `busy`, `error` |
| `gobb_signin_refusals_total` | counter: tries that the sign-in schedule refused before any password check | `reason`: `address`, `name`, `defensive` |
| `gobb_signin_schedule_evictions_total` | counter: counts that a full table of the sign-in schedule dropped | `table`: `address`, `name` |
| `gobb_defensive_mode` | gauge: 1 while Defensive Mode is on, 0 while it is off | none |
| `gobb_rate_limited_total` | counter | `limit`: the name of each limit, such as `search` or `reset_account` |
| `gobb_rate_limit_allowed_total` | counter | `limit` |
| `gobb_rate_limit_fill_ratio` | histogram, buckets 0.25, 0.5, 0.75, 0.9, 1 | `limit` |
| `gobb_rate_limit_refunds_total` | counter | `limit` |
| `gobb_rate_limit_evictions_total` | counter | `limit` |
| `gobb_rate_limit_keys`, `_keys_full`, `_fullest_ratio` | gauges, read at each scrape | `limit` |
| `gobb_rate_limit_max_events`, `_window_seconds`, `_max_keys` | gauges, the settings of the limit | `limit` |
| `gobb_emails_total` | counter | `result`: `sent`, `failed` (to be tried again), `dropped` |
| `gobb_reset_emails_skipped_total` | counter | `reason`: `board_hour`, `board_day`, `account_day`, `admin`, `ineligible` |
| `gobb_email_queue_depth` | gauge | none |
| `gobb_sessions_active` | gauge | none |
| `gobb_signin_floor_seconds` | gauge | none |
| `gobb_memory_gate_holders`, `_waiting`, `_used_bytes`, `_budget_bytes` | gauges | none |
| `gobb_db_pool_conns`, `_acquired_conns`, `_idle_conns`, `_max_conns` | gauges | none |
| `gobb_db_pool_acquires_total`, `_empty_acquires_total`, `_canceled_acquires_total`, `_acquire_seconds_total`, `_new_conns_total` | counters | none |
| `gobb_db_pool_wait_timeouts_total` | counter: requests whose wait for a connection ran out (`GOBB_DB_POOL_WAIT`) | none |
| `gobb_search_busy_total` | counter: searches refused because `GOBB_SEARCH_MAX_RUNNING` searches were running | none |
| `gobb_in_flight_refusals_total` | counter: requests refused because their in-flight limit was full | `limit`: `all` (`GOBB_MAX_IN_FLIGHT`), `signin` (`GOBB_MAX_SIGNIN_IN_FLIGHT`) |
| `gobb_page_cache_hits_total`, `_misses_total` | counters: guest page requests served from the cache (including those served before the in-flight limit), and those that waited for a render | none |
| `gobb_page_cache_evictions_total` | counter: pages dropped to stay under `GOBB_PAGE_CACHE_MIB` | none |
| `gobb_page_cache_drops_total` | counter: drops of pages after a change to the board | none |
| `gobb_page_cache_bytes`, `_entries`, `_limit_bytes` | gauges: the size of the cache, its pages, and its limit | none |
| `gobb_page_cache_warm_pages_total`, `_warm_passes_total` | counters: pages that the start-up render stored, and its passes | none |
| `gobb_page_cache_warm_running` | gauge: 1 while a pass of the start-up render runs | none |
| `gobb_batch_flushes_total`, `gobb_batch_flush_errors_total` | counters: flushes of the batched writes that wrote, and that failed | none |
| `gobb_batch_pending` | gauge: writes that wait for the next flush | none |
| `gobb_build_info` | gauge, value 1 | `version`, `commit` |
| `go_goroutines`, `go_memstats_heap_alloc_bytes`, `go_memstats_sys_bytes`, `go_gc_heap_goal_bytes`, `go_memory_limit_bytes`, `go_gc_cycles_total` | Go runtime | none |
| `process_start_time_seconds`, `process_cpu_seconds_total`, `process_resident_memory_bytes` (Linux) | process | none |

A label holds a fixed value only. `route` is the pattern that answered, without its
method (`/t/{id}`), and never the path. `none` is a request that no pattern took.
`method` is `GET`, `HEAD`, `POST` or `other`. `code` is the status class, `2xx` to
`5xx`. `gobb_rate_limited_total` counts each request that a limit refused. The sign-in
schedule is not one of these limits: `gobb_signin_refusals_total` counts its refusals.
A sign-in at a locked name from one of the member's trusted addresses is no refusal.

The other `gobb_rate_limit_` metrics show how full each limit gets, in the same `limit`
label (15 names, never a key). `gobb_rate_limit_allowed_total` counts the events that a
limit recorded. `gobb_rate_limit_fill_ratio` has one observation for each of them: the
events of that key in the window after it, over the limit, so `1` is a key at the limit.
`gobb_rate_limit_refunds_total` counts the events that a refund gave back. A refund
lowers neither of the two above, so the net events recorded are allowed minus refunds,
and the histogram is the fill at the time of the check, refunded or not.
`gobb_rate_limit_evictions_total` counts the keys, with events in the window, that a
full table dropped to make room;
those keys start again at zero. A value above 0 means the table is too small for the
traffic. A key with no event in the window is dropped with no count. The gauges
`gobb_rate_limit_keys` (keys held, live or not), `gobb_rate_limit_keys_full` (keys at
the limit now) and `gobb_rate_limit_fullest_ratio` (the highest fill of any key now)
come from one scan of the keys of each limit at each scrape, so they agree. The scan
holds the lock of the limit while it reads at most the keys of the table. The share of
refusals is `gobb_rate_limited_total` over the sum of that and
`gobb_rate_limit_allowed_total`. The queue and session gauges each run one query
at most every 15 s; a failed query leaves the gauge out of the scrape and logs one
WARN line. CI runs `promtool check metrics` on a scrape.

## Release

A release is a tag `vX.Y.Z`. The release workflow publishes binaries for `linux/amd64`,
`linux/arm64`, `darwin/amd64` and `darwin/arm64` with a SHA-256 sum for each, the image
`ghcr.io/evanwtf/gobb` as `:X.Y.Z` and `:latest` for `linux/amd64` and `linux/arm64`,
and a GitHub release. The release notes are the section of `docs/changelog.md` for the
version, never the message of the tag.

1. In `docs/changelog.md`, change the date of the first section from `(unreleased)` to
   the release date, as `## X.Y.Z (YYYY-MM-DD)`. `internal/version` must hold `X.Y.Z`.
2. Check it, and commit and push to `main`:

   ```sh
   export TAG=v0.1.0
   go run ./scripts/releasecheck "$TAG"
   ```

3. Tag and push the tag:

   ```sh
   git tag -a "$TAG" -m "gobb $TAG" && git push origin "$TAG"
   ```

4. The first push of the image makes a private package. After the release run, open the
   package settings of `ghcr.io/evanwtf/gobb` and make it public. Until then, `docker
   compose pull` fails for everyone who is not signed in to the registry.

`releasecheck` is the first job of the workflow. It refuses a tag that is not `vX.Y.Z`,
a first section that is not exactly `X.Y.Z` and a date, a version in `internal/version`
that differs from the tag, an empty section, and notes over 125,000 characters (the
limit of GitHub for a release body). In a tag run, the job also refuses a tag whose
commit is not on `main`, and a tag that already has a release, so a re-run of a
published tag cannot push the images again. Nothing builds or publishes after a
refusal.

To try the workflow without a release, run it by hand with `dry_run` true, which is the
default. It builds every artifact and runs the local image, and it pushes nothing and
creates no release. The dry run accepts `(unreleased)` as the date. A manual run with
`dry_run` false refuses unless the ref is a `vX.Y.Z` tag.

```sh
gh workflow run release.yml --ref main -f dry_run=true
```

## Status

Plan 1, the foundation, is built. It ships the repo guards and CI, the configuration,
the database pool and migrations, the release-1 schema, legacy and gobb passwords, the
permission registry, the public `markup` package (Markdown parser, renderer, embeds and
tree comparison), and the container image with Compose and a health check. Plan 2 adds
the bundle format and the importer.

Plan 3, the read side, is built. `gobb serve` shows an imported board, read-only: the
board index, forums, topics and single posts, profiles, the member list, the team page,
"New since your last visit" with read marks, search, static pages, attachments and the
other data files. Members sign in and out and pick a color scheme. Old addresses
redirect to the new ones, and a gone member's profile answers 410. Every page carries
the security headers and a Content-Security-Policy, and every error page uses the
board's layout.

Plan 4, the write side, is built for v0.1.0: members post, edit and moderate. v0.2.0
adds private messages and the account settings (roadmap row 4b). v0.3.0 adds email and
the password reset by email (row 4c). v0.4.0 adds `gobb user activate` and
`gobb user deactivate`. v0.5.0 adds the metrics listener. v0.6.0 adds the rate limit metrics. v0.7.0 shows private messages as conversations. v0.8.0 adds the admin panel. Attachment uploads and email notifications come
later. The design is in
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
