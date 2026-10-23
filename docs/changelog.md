# Changelog

## 0.1.0 (unreleased)

- Scaffold the Go module, the `gobb version` command, and the repo guards.
- Add the permission registry (48 options) and NEVER-wins resolution in `internal/acl`.
- Add `gobb serve` with a `/healthz` check, `gobb healthcheck`, a Chainguard image and a Compose file.
- Add the `bbcode` package: the document tree, its schema validator, and embed ID rules.
- Add the legacy password form, `password.LegacyForm`, in `internal/password`.
- Add `password.VerifyLegacy` for bcrypt, argon2id and chain hashes from the imported board, with the portable-hash digest and a strict argon2id PHC parser.
