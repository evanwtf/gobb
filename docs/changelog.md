# Changelog

## 0.1.0 (unreleased)

- Scaffold the Go module, the `gobb version` command, and the repo guards.
- Add the permission registry (48 options) and NEVER-wins resolution in `internal/acl`.
- Add `gobb serve` with a `/healthz` check, `gobb healthcheck`, a Chainguard image and a Compose file.
- Add the `bbcode` package: the document tree, its schema validator, and embed ID rules.
- Reshape `bbcode` into `markup` for Markdown posts: new block and inline element set, site paths and `mailto:` links through `CheckLinkURL` and `CheckImageURL`, `IsEmbed`, and embed IDs for Reddit, TikTok, Threads, Spotify and SoundCloud.
- Add the legacy password form, `password.LegacyForm`, in `internal/password`.
- Add `password.VerifyLegacy` for bcrypt, argon2id and chain hashes from the imported board, with the portable-hash digest and a strict argon2id PHC parser.
- Rename the post and PM flags to `enable_markdown` and `enable_autolink`, and the permissions to `post.markdown` and `pm.markdown`. Drop the `tag_aliases` table.
- Add `password.Hash` and `password.Verify` for gobb's own argon2id hashes, with a signal to rehash legacy hashes and hashes with old parameters.
