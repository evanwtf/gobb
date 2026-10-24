# Changelog

## 0.1.0 (unreleased)

- Scaffold the Go module, the `gobb version` command, and the repo guards.
- Add the permission registry (48 options) and NEVER-wins resolution in `internal/acl`.
- Add `gobb serve` with a `/healthz` check, `gobb healthcheck`, a Chainguard image and a Compose file.
- Add the `bbcode` package: the document tree, its schema validator, and embed ID rules.
- Reshape `bbcode` into `markup` for Markdown posts: new block and inline element set, site paths and `mailto:` links through `CheckLinkURL` and `CheckImageURL`, `IsEmbed`, and embed IDs for Reddit, TikTok, Threads, Spotify and SoundCloud.
- Pin the `markup` URL contract: the renderer emits the trimmed string the check approved, HTML-escaped; reject format characters in URLs, `?#%&/` in mail addresses, control characters in `alt`, `display` and smiley codes, and dot-only Threads users.
- Add the legacy password form, `password.LegacyForm`, in `internal/password`.
- Add `password.VerifyLegacy` for bcrypt, argon2id and chain hashes from the imported board, with the portable-hash digest and a strict argon2id PHC parser.
- Rename the post and PM flags to `enable_markdown` and `enable_autolink`, and the permissions to `post.markdown` and `pm.markdown`. Drop the `tag_aliases` table.
- Add `password.Hash` and `password.Verify` for gobb's own argon2id hashes, with a signal to rehash legacy hashes and hashes with old parameters.
- Add `markup.Render`: an allowlist renderer that turns a tree into HTML, checks each `href` and `src` again and emits the value it checked, renders the nine embeds, and reports the frame and script origins they need.
- Add `markup.Parse` and the `goldmark` dependency (MIT, no dependencies of its own). CommonMark has hundreds of edge cases, and a tested parser makes posts behave the way members expect from other sites. The `||spoiler||` delimiter, the embed split, smilies and the nesting caps live in this package. Quote, list and list item parsers stop opening at 32 nested containers, because goldmark time grows with the square of the nesting on one line.
- Fix `markup.Render`: percent-encode a `mailto:` address in a `url` element, as in `email`; do not decode entities in an `email` address; list `platform.twitter.com` and `platform.x.com` as tweet script origins, because the first redirects to the second; upgrade smiley image URLs to https; end the permalink of a Threads post with `/`; replace the node cap with a set of visited nodes, so a valid tree of any size renders in full and a cycle ends.
- Make `markup` URLs literal: `CheckLinkURL`, `CheckImageURL` and the renderer no longer decode HTML entities in `href` or `src`, which turned `&copy=` into a copyright sign and `&shy=` into a rejected link. Cap only the quote and list parsers in `markup.Parse`, so a capped list always keeps its item and no input panics. Drop smiley codes that `Validate` would reject. Reject format characters and backslash in mail addresses.
- Add `markup.Compare` and `markup.Diff`: a converter compares the tree it expects with `Parse` of the Markdown it writes, and gets the path, the wanted value and the found value of each difference. Hand-written Markdown and tree fixtures pin the element rules. `FuzzRender` now also parses Markdown, and checks that every `href` and `src` in the output passes the URL checks.
