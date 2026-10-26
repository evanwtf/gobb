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

`gobb import [--replace] <bundle>` loads a bundle into the database and the data
volume. The bundle is a directory or a `.tar.gz` file. Put the flag before the path.
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

The restore drops the `gobb` database, creates it again, and loads the dump into the
empty database. It does not use `pg_restore --clean`: that option drops only the tables
that are in the dump, so a table that a newer migration added would stay, and the next
upgrade would try to create it again. The restore then empties the data volume and
unpacks the archive.

Set the folder of the backups first. This is the line of the backup section. Change it
if your backups live in another folder, because a new shell does not keep it:

```sh
export BACKUP_DIR="$HOME/gobb-backups"
```

The first command picks the newest stamp for which both files exist and are not empty.
To restore an older backup, set `STAMP` to its date.

<!-- block: restore-pick -->
```sh
export STAMP=$(ls "$BACKUP_DIR" | sed -n 's/^gobb-\([0-9]*-[0-9]*\)\.dump$/\1/p' | while read -r s; do [ -s "$BACKUP_DIR/gobb-$s.dump" ] && [ -s "$BACKUP_DIR/gobb-data-$s.tar.gz" ] && echo "$s"; done | tail -n 1)
```

The second command is one chain, and the first failure stops it. Nothing is stopped or
deleted until these checks pass: `STAMP` is not empty, the dump and the archive both
exist and are not empty, `pg_restore --list` reads the dump, and `tar tzf` reads the
archive. Each check prints its own message on stderr when it fails, and the chain exits
with a non-zero status. Read the message, fix the cause and run it again. The
`postgres` service must be running.

<!-- block: restore -->
```sh
{ test -n "$STAMP" || { echo "STAMP is empty: no backup in $BACKUP_DIR has both a dump and an archive" >&2; false; }; } && \
{ test -s "$BACKUP_DIR/gobb-$STAMP.dump" || { echo "no dump for $STAMP in $BACKUP_DIR" >&2; false; }; } && \
{ test -s "$BACKUP_DIR/gobb-data-$STAMP.tar.gz" || { echo "no archive for $STAMP in $BACKUP_DIR" >&2; false; }; } && \
{ docker compose exec -T postgres pg_restore --list < "$BACKUP_DIR/gobb-$STAMP.dump" > /dev/null || { echo "pg_restore cannot read the dump for $STAMP" >&2; false; }; } && \
{ tar tzf "$BACKUP_DIR/gobb-data-$STAMP.tar.gz" > /dev/null || { echo "tar cannot read the archive for $STAMP" >&2; false; }; } && \
docker compose stop gobb && \
docker compose exec -T postgres psql -U gobb -d postgres -v ON_ERROR_STOP=1 -c 'drop database if exists gobb with (force)' -c 'create database gobb' && \
docker compose exec -T postgres pg_restore -U gobb -d gobb --exit-on-error < "$BACKUP_DIR/gobb-$STAMP.dump" && \
docker run --rm --volumes-from "$(docker compose ps -aq gobb)" -v "$BACKUP_DIR:/backup:ro" \
  --entrypoint sh postgres:18 \
  -c 'tar tzf "/backup/gobb-data-$1.tar.gz" > /dev/null && find /var/lib/gobb -mindepth 1 -delete && tar xzf "/backup/gobb-data-$1.tar.gz" -C /var/lib/gobb' sh "$STAMP"
```

Then set `GOBB_VERSION` back to the version that wrote the backup, and run
`docker compose up -d --wait`. gobb reads the legacy routes and the stored password
hashes once at start, so always start it after a restore.

CI runs these commands on every push. It restores a dump over a database that holds an
extra table, and checks that the table is gone and that the row count of every table is
the same (`scripts/rowcounts`). It also runs the restore with the archive missing, and
checks that the restore deletes nothing and fails.

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

The server reads these variables. Docker Compose reads the last one:

| Variable | Meaning | Default |
|---|---|---|
| `GOBB_DSN` | PostgreSQL connection string. Required. | none |
| `GOBB_LISTEN` | Address to bind. | `127.0.0.1:8080` |
| `GOBB_DATA_DIR` | Directory for avatars, smilies, ranks, the header image and attachments. | `/var/lib/gobb` |
| `GOBB_BASE_PATH` | URL path prefix, such as `/forum/`. | `/` |
| `GOBB_TRUSTED_PROXIES` | Comma-separated CIDRs of the proxies to trust. | `127.0.0.1/32,::1/128` |
| `GOBB_COOKIE_SECURE` | `true` or `false`. Mark the cookies Secure. | `true` |
| `GOBB_SMTP_HOST`, `GOBB_SMTP_PORT`, `GOBB_SMTP_TLS`, `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD`, `GOBB_MAIL_FROM`, `GOBB_PUBLIC_URL` | Email settings (see "Email"). | email off |
| `GOMEMLIMIT` | The Go memory limit, such as `180MiB`. Unset, gobb sets 70% of the cgroup memory limit (see "Memory"). | unset |
| `GOBB_MEM_LIMIT` | Compose only: the memory cap of the gobb container, with no swap on top. | `256m` |

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
| `GOBB_SMTP_HOST` | The SMTP server. Turns email on. With no host, any other variable in this table is an error. | unset |
| `GOBB_SMTP_PORT` | The port, 1 to 65535. | `587` for `starttls`, `465` for `implicit` |
| `GOBB_SMTP_TLS` | `starttls` or `implicit`. There is no plain mode. | `starttls` |
| `GOBB_SMTP_USER`, `GOBB_SMTP_PASSWORD` | Both or neither. gobb signs in with AUTH PLAIN, and only after TLS runs. | unset |
| `GOBB_MAIL_FROM` | The sender: one plain address, with no display name. Required with a host. | none |
| `GOBB_PUBLIC_URL` | The address of the board, for links in email. It starts with `https://`, names a host, holds no user information, query or fragment, and its path equals `GOBB_BASE_PATH`. It starts with `http://` only when `GOBB_COOKIE_SECURE=false`. gobb stores it with a trailing `/`. Required with a host. | none |

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

### Reset a password, ban or unban a member

A member resets the password by email. With email on (see "Email" above), the
sign-in page shows "Forgot your password?". The link opens a form for the username
or the email address, and gobb emails a link that works once, for 1 hour. Opening
the link changes nothing; saving a new password ends every session of the member.
With email off, the link and the pages do not exist.

The administrator resets a password from the command line. Run the commands where
the database is. With Docker Compose, run them in the running container. The `-T` flag keeps Compose from adding a
terminal, which would change the line endings:

```sh
export NAME=alice
docker compose exec -T gobb gobb user reset-password "$NAME"
docker compose exec -T gobb gobb user ban "$NAME" -reason "spam run"
docker compose exec -T gobb gobb user unban "$NAME"
```

Without Docker, run `./gobb user ...` with `GOBB_DSN` set, as for `gobb import`.

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

Each command that changes something writes one row to the moderation log (`user.reset_password`,
`user.ban`, `user.unban`) with no actor and `{"via":"cli"}` as details. Messages go to
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
- A failed sign-in takes at least the floor, even when the client closes its end of
  the connection early. This holds for every failure after the limits pass: a wrong
  password, a busy server, an inactive account and a ban. The floor counts from the
  start of the sign-in, moved forward by the time that it waits for the memory gate.
  So a wait for the gate adds the same delay to every name, and the floor still
  hides the lookup and the hash after it. The server sets the floor at start:
  2 × K × T, but at least 400 ms, rounded up to a whole 50 ms. T is the slowest
  single password check, which the server times once for each kind and cost of
  stored hash, with a wrong password of 4,096 bytes (the longest that a check reads;
  a chain check takes longer for a longer password). K is the most accounts that
  share one email address, because a sign-in by that address checks each of them.
  The start-up log line `login: sign-in floor` gives the floor, T and K. The floor
  hides the time of the hash while the host runs a check within twice its start-up
  time. Heavier CPU load can still show the time.
- A sign-in holds the memory gate once, for all of its checks. So a busy server
  answers alike for a shared email address, a unique one and an unknown name.
- Password checks share a memory gate of 128 MiB, or less under a low memory limit
  (see "Memory"). Every check holds the same share,
  whatever the kind of hash, so neither the wait nor the answer tells a stranger
  which names have accounts or which accounts have old hashes. The share follows the
  most costly stored hash, which the server reads at start. With no hash above the
  cost of a new gobb hash (19 MiB), six checks run at once. On a board with imported
  64 MiB argon2id hashes, two checks run at once. The start-up log line
  `login: password check charge` gives the share in KiB and the number of checks at
  once. A sign-in replaces an old hash with a gobb hash, so the share falls at a
  later start, when no costly hash is left. A check that finds no room for 5
  seconds answers 503.

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
also times one check of each kind of stored hash, inside the gate, and then frees the
memory of those checks.

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

Plan 4, the write side, is built for v0.1.0: members post, edit and moderate. Before the
cutover, members also get private messages, a time zone setting and avatar upload
(roadmap row 4b). Attachment uploads and email come later. The design is in
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
