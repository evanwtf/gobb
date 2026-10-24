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

`gobb serve` applies the pending migrations at startup, then serves `GET /healthz`.
It stops cleanly on SIGTERM. Set `GOBB_PORT` in `.env` if port 8080 is busy.

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

Stop the server first. The import lock keeps two imports apart, but it does not stop
the server from writing to the board during an import.

```sh
export BUNDLE=/path/to/bundle.tar.gz
docker compose stop gobb
docker compose run --rm -v "$BUNDLE:/import/bundle.tar.gz:ro" gobb import /import/bundle.tar.gz
docker compose start gobb
```

The container runs as the `nonroot` user, so the bundle file must be readable by
everyone (mode 0644). The command applies the pending migrations, then prints the
number of rows it loaded from each data file, and the number of files it staged.

The import refuses a database that holds data. `--replace` empties the database and
moves the old files to `.replaced-<time>/` in the data volume. It deletes nothing on
disk.

A converter packs a bundle directory like this:

```sh
export DIR=/path/to/bundle-directory
tar -C "$DIR" -czf bundle.tar.gz .
```

On macOS, write `COPYFILE_DISABLE=1 tar -C "$DIR" -czf bundle.tar.gz .`. Without it,
macOS `tar` adds `._` files, and the importer refuses them.

To check the permissions after an import, write them as JSON Lines to a file. The
`-T` flag keeps Compose from adding a terminal, which would change the line endings.

```sh
docker compose run --rm -T gobb perms export > perms.jsonl
```

## Status

Plan 1, the foundation, is built. It ships the repo guards and CI, the configuration,
the database pool and migrations, the release-1 schema, legacy and gobb passwords, the
permission registry, the public `markup` package (Markdown parser, renderer, embeds and
tree comparison), and the container image with Compose and a health check. The server
answers `GET /healthz` and nothing else yet: the forum pages come in later plans. The
design is in
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
