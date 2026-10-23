# Changelog

## 0.1.0 (unreleased)

- Scaffold the Go module, the `gobb version` command, and the repo guards.
- Add the permission registry (48 options) and NEVER-wins resolution in `internal/acl`.
- Add `gobb serve` with a `/healthz` check, `gobb healthcheck`, a Chainguard image and a Compose file.
- Add the `bbcode` package: the document tree, its schema validator, and embed ID rules.
- Reshape `bbcode` into `markup` for Markdown posts: new block and inline element set, site paths and `mailto:` links through `CheckLinkURL` and `CheckImageURL`, `IsEmbed`, and embed IDs for Reddit, TikTok, Threads, Spotify and SoundCloud.
- Pin the `markup` URL contract: the renderer emits the decoded, trimmed string the check approved; reject format characters in URLs, `?#%&/` in mail addresses, control characters in `alt`, `display` and smiley codes, and dot-only Threads users.
- Add the legacy password form, `password.LegacyForm`, in `internal/password`.
- Add `password.VerifyLegacy` for bcrypt, argon2id and chain hashes from the imported board, with the portable-hash digest and a strict argon2id PHC parser.
- Rename the post and PM flags to `enable_markdown` and `enable_autolink`, and the permissions to `post.markdown` and `pm.markdown`. Drop the `tag_aliases` table.
- Add `password.Hash` and `password.Verify` for gobb's own argon2id hashes, with a signal to rehash legacy hashes and hashes with old parameters.
- Add `markup.Render`: an allowlist renderer that turns a tree into HTML, checks each `href` and `src` again and emits the value it checked, renders the nine embeds, and reports the frame and script origins they need.
- Add `markup.Parse` and the `goldmark` dependency (MIT, no dependencies of its own). CommonMark has hundreds of edge cases, and a tested parser makes posts behave the way members expect from other sites. The `||spoiler||` delimiter, the embed split, smilies and the nesting caps live in this package. Quote, list and list item parsers stop opening at 32 nested containers, because goldmark time grows with the square of the nesting on one line.
