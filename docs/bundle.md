# The gobb import bundle, format version 1

A bundle is the file format that a converter writes to move a board into gobb. This
page is the reference for the format. The Go package `github.com/evanwtf/gobb/bundle`
holds the same format as types, and it checks a bundle without a database.

"The source system" means the forum software that a converter reads from. gobb does
not guess at data it does not model. Unknown fields, unknown enum values, broken
references and a count that disagrees with the manifest all stop the import.

## Layout

A bundle is a directory, or a gzip-compressed tar archive of that directory. It holds
these entries, and nothing else may appear at the top level:

- `manifest.json`
- `settings.json`
- 23 data files, each named `<name>.jsonl`. Each file is present even when it is empty.
- `files/`, the directory that holds the files the rows name.

A data file holds one JSON object per line. A line is at most 1 MiB (1,048,576 bytes).
The line must be valid UTF-8. Every error that a bundle causes names the file, the line
and the field, for example `posts.jsonl:12: body.source: is 100001 bytes; the limit is 100000`.

The data files, in load order:

1. `ranks.jsonl`
2. `users.jsonl`
3. `gone_users.jsonl`
4. `groups.jsonl`
5. `group_members.jsonl`
6. `roles.jsonl`
7. `grants.jsonl`
8. `forums.jsonl`
9. `topics.jsonl`
10. `posts.jsonl`
11. `polls.jsonl`
12. `poll_votes.jsonl`
13. `attachments.jsonl`
14. `pms.jsonl`
15. `topic_watches.jsonl`
16. `bookmarks.jsonl`
17. `topic_marks.jsonl`
18. `forum_marks.jsonl`
19. `bans.jsonl`
20. `smilies.jsonl`
21. `legacy_routes.jsonl`
22. `legacy_files.jsonl`
23. `static_pages.jsonl`

## Field rules

Field names are the JSON names. A field with a `?` after its name in a table may be
`null`. Every other field is required. A missing field and an unknown field are both
errors.

- **id**: a positive integer (1 to 9,223,372,036,854,775,807).
- **count**: an integer of 0 or more.
- **int**: any integer. A `position` is an int. It sets the order of rows.
- **time**: an RFC 3339 string with an offset, for example `2020-03-04T05:06:07Z` or
  `2020-03-04T00:06:07-05:00`. `Z` counts as an offset.
- **string**: valid UTF-8, with no U+0000, and at most 100,000 bytes unless the table
  says less.
- **name**: 1 to 255 bytes after gobb trims spaces from both ends, with no control
  characters.
- **label**: at most 255 bytes, with no control characters. It may be empty.
- **slug**: matches `^[a-z0-9][a-z0-9-]{0,63}$`.
- **param**: matches `^[A-Za-z0-9_]{1,64}$`.
- **ip**: an IPv4 or IPv6 address, with no zone, for example `10.0.0.5` or `2001:db8::5`.
- **zone**: an IANA time zone name that Go's `time.LoadLocation` accepts, for example
  `America/New_York`. The empty string and `Local` are not zone names here.
- **setting**: `"yes"`, `"no"` or `"never"`. `never` always wins over `yes`.
- **option**: one of the permission option names that gobb defines, such as
  `forum.read` or `topic.reply`. An option is one of three kinds. A *forum* option
  applies per forum. A *board* option applies to the whole board. A *moderator* option
  applies at both levels.

### Shared shapes

**file**: names a file under `files/`.

| Field | Type | Rule |
|---|---|---|
| `path` | string | A slash-separated path. It passes Go's `fs.ValidPath`, holds no backslash, and is at most 255 bytes. Its first element is the directory that the field names, and it names a file under that directory. |
| `size` | count | The size of the file in bytes. |
| `sha256` | string | 64 lowercase hex characters. |

**body**: the content of a post, a private message, a signature or a static page.

| Field | Type | Rule |
|---|---|---|
| `source` | string | The Markdown source, at most 100,000 bytes. |
| `markdown` | bool | Whether Markdown applies to this content. |
| `smilies` | bool | Whether smiley codes turn into images. |
| `autolink` | bool | Whether bare web addresses turn into links. |

gobb computes the rendered form of every body itself. A converter writes the source and
the three flags only.

### Links in content

A bundle may point at gobb pages with these site paths. Each path is relative to the
board's base path: with base path `/forum/`, the path `/p/5` means `/forum/p/5`.

| Path | Target |
|---|---|
| `/p/<post id>` | the post, on its topic page (`#p<id>`) |
| `/t/<topic id>` | the topic |
| `/f/<forum id>` | the forum |
| `/u/<user id>` | the profile |
| `/a/<attachment id>` | the attachment's file. An image whose URL is this path shows the attachment inline. |

### Files

Every file that a row names exists under `files/` as a regular file, with the stated
size and SHA-256. Rows that name the same path agree on size and SHA-256. Every file
under `files/` is named by at least one row.

gobb copies each file to `<data dir>/<path>`. It owns five directories there:
`attachments/`, `avatars/`, `smilies/`, `ranks/` and `header/`. A path in a field is the
same path as in `files/`, for example `attachments/12.png`.

### Values that gobb computes

A converter does not write these, because gobb derives them:

- `forum_id` of a post, from its topic;
- the rendered form and the search text of every body;
- the cleaned form of a user name and of an email address;
- the topic and forum counters, and the last-post pointers.

The bundle does carry the numbers that gobb cannot rebuild: `post_count` of a user,
`view_count` of a topic, `votes` of a poll option and `download_count` of an
attachment.

## manifest.json

| Field | Type | Rule |
|---|---|---|
| `format` | string | The text `gobb-bundle`. |
| `version` | int | The number `1`. |
| `created_at` | time | When the converter wrote the bundle. |
| `source` | label | A short description of the source system. |
| `counts` | object | Exactly the 23 data-file names as keys (no `.jsonl`). Each value is a count: the number of lines in that file. |

A count that disagrees with its file stops the import.

```json
{
  "format": "gobb-bundle",
  "version": 1,
  "created_at": "2026-01-15T12:00:00Z",
  "source": "example forum export",
  "counts": {
    "ranks": 1, "users": 2, "gone_users": 1, "groups": 4, "group_members": 1,
    "roles": 1, "grants": 2, "forums": 2, "topics": 1, "posts": 2, "polls": 1,
    "poll_votes": 1, "attachments": 1, "pms": 1, "topic_watches": 1, "bookmarks": 1,
    "topic_marks": 1, "forum_marks": 1, "bans": 1, "smilies": 1, "legacy_routes": 1,
    "legacy_files": 1, "static_pages": 1
  }
}
```

## settings.json

| Field | Type | Rule |
|---|---|---|
| `board_name` | name | The name of the board. |
| `board_description` | label | A short description. |
| `timezone` | zone | The zone in which guests see times. |
| `default_color_scheme` | string | `slate`, `slate-deep` or `slate-light`. |
| `registration` | string | `closed`, `open` or `approval`. |
| `email_notifications` | bool | Whether the board sends notification email. |
| `header_image`? | file | An image under `header/`, or `null`. It holds `path`, `size` and `sha256`. |

```json
{
  "board_name": "Example Board",
  "board_description": "A board for examples",
  "timezone": "America/New_York",
  "default_color_scheme": "slate",
  "registration": "open",
  "email_notifications": true,
  "header_image": {"path": "header/banner.png", "size": 1, "sha256": "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"}
}
```

## ranks.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `title` | label | The rank name that members see. |
| `min_posts` | count | The post count at which a member earns the rank. |
| `special` | bool | Marks a special rank. |
| `image`? | file | An image under `ranks/`, or `null`. It holds `path`, `size` and `sha256`. |

```json
{"id":1,"title":"Regular","min_posts":50,"special":false,"image":{"path":"ranks/regular.png","size":1,"sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"}}
```

## users.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `username` | name | |
| `email` | label | May be empty. |
| `password` | object | `scheme`: `legacy` or `gobb`. `hash`: the stored hash. gobb must recognize the pair as a hash it can verify. |
| `state` | string | `active` or `inactive`. |
| `owner` | bool | Whether the user is a board owner. |
| `email_verified` | bool | |
| `registered_at` | time | |
| `last_visit_at`? | time | |
| `last_mark_at`? | time | |
| `post_count` | count | |
| `rank_id`? | id | A row of `ranks.jsonl`. |
| `avatar`? | file | An image under `avatars/`. It holds `path`, `size` and `sha256`. |
| `signature` | body | The user's signature. It holds `source`, `markdown`, `smilies` and `autolink`. |
| `timezone` | zone | |
| `color_scheme` | string | `slate`, `slate-deep` or `slate-light`. |
| `notify_on_post` | bool | |

Cross-row rules: user names are unique after gobb cleans them (case folding and
Unicode normalization). At least one active user is an owner.

```json
{"id":1,"username":"alice","email":"alice@example.com","password":{"scheme":"gobb","hash":"$argon2id$v=19$m=19456,t=2,p=1$nIWkXeM7L5FA7XDKfH36lQ$lxqRFYzVFEIy5o0i5Or7n6g0FyBsBN3Mfl8PA1WJdMo"},"state":"active","owner":true,"email_verified":true,"registered_at":"2019-05-01T10:00:00Z","last_visit_at":"2026-01-10T08:30:00Z","last_mark_at":null,"post_count":120,"rank_id":1,"avatar":null,"signature":{"source":"Example signature","markdown":true,"smilies":true,"autolink":true},"timezone":"America/New_York","color_scheme":"slate","notify_on_post":true}
```

## gone_users.jsonl

A row records the ID of a user that the source system deleted.

| Field | Type | Rule |
|---|---|---|
| `id` | id | Not also the ID of a user. |

```json
{"id":900}
```

## groups.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `name` | name | |
| `builtin`? | string | `guests`, `members`, `moderators` or `admins`, or `null`. |
| `kind` | string | `open`, `request`, `closed`, `hidden` or `builtin`. |
| `description` | string | |

`kind` is `builtin` exactly when `builtin` is set. Each of the four built-in names
appears exactly once in the file.

```json
{"id":1,"name":"Guests","builtin":"guests","kind":"builtin","description":"Visitors who are not signed in"}
```

## group_members.jsonl

| Field | Type | Rule |
|---|---|---|
| `group_id` | id | A row of `groups.jsonl`. |
| `user_id` | id | A row of `users.jsonl`. |
| `leader` | bool | Whether the user leads the group. |
| `pending` | bool | Whether the membership waits for approval. |

The pair (`group_id`, `user_id`) is unique.

```json
{"group_id":2,"user_id":1,"leader":false,"pending":false}
```

## roles.jsonl

A role is a named set of option settings. A grant can give a whole role at once.

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `name` | name | |
| `kind` | string | `forum`, `mod` or `board`. |
| `description` | string | |
| `position` | int | |
| `settings` | list | Each item has `option` (an option) and `setting` (a setting). |

Each option appears at most once in a role. A `forum` role holds forum options only. A
`mod` role holds moderator options only. A `board` role holds board options only.

```json
{"id":1,"name":"Read only","kind":"forum","description":"May list and read","position":1,"settings":[{"option":"forum.list","setting":"yes"},{"option":"forum.read","setting":"yes"}]}
```

## grants.jsonl

A grant gives a group or a user one option setting, or a whole role, in one forum or
on the whole board.

| Field | Type | Rule |
|---|---|---|
| `group_id`? | id | A row of `groups.jsonl`. |
| `user_id`? | id | A row of `users.jsonl`. Set exactly one of `group_id` and `user_id`. |
| `forum_id` | int | `0` means the whole board. Otherwise a row of `forums.jsonl`. |
| `role_id`? | id | A row of `roles.jsonl`. |
| `option`? | option | The option name. |
| `setting`? | setting | The setting for `option`. |

Set exactly one of `role_id` and the pair `option` and `setting`. A grant of an option
fits its scope: a forum option needs a `forum_id` that names a forum, a board option
needs `0`, and a moderator option fits both. A grant of a role fits the same way by the
kind of the role: a `forum` role needs a forum, a `board` role needs `0`, and a `mod`
role fits both.

```json
{"group_id":2,"user_id":null,"forum_id":2,"role_id":null,"option":"forum.read","setting":"yes"}
```

## forums.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `parent_id`? | id | A row of `forums.jsonl`. The chain of parents has no cycle. |
| `kind` | string | `category`, `forum` or `link`. |
| `name` | name | |
| `description` | string | |
| `rules` | string | |
| `link_url`? | string | The target of a link forum. |
| `locked` | bool | |
| `position` | int | |

`link_url` is set exactly when `kind` is `link`. It must be an `http` or `https`
address, a `mailto:` address, or a site path.

```json
{"id":2,"parent_id":null,"kind":"forum","name":"General","description":"Talk about anything","rules":"Be kind.","link_url":null,"locked":false,"position":1}
```

## topics.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `forum_id` | id | A row of `forums.jsonl` whose `kind` is `forum`. |
| `title` | label | |
| `author_id`? | id | A row of `users.jsonl`, or `null`. |
| `author_name` | label | The name to show. |
| `created_at` | time | |
| `view_count` | count | |
| `kind` | string | `normal`, `sticky`, `announce` or `global`. |
| `locked` | bool | |
| `visibility` | string | `approved`, `unapproved` or `deleted`. |
| `moved_to`? | id | A row of `topics.jsonl`. |

A topic with `moved_to` is a marker for a moved topic. It has no posts, and its target
is not itself a marker. Every other topic has at least one post.

```json
{"id":1,"forum_id":2,"title":"Welcome","author_id":1,"author_name":"alice","created_at":"2019-05-02T09:00:00Z","view_count":340,"kind":"sticky","locked":false,"visibility":"approved","moved_to":null}
```

## posts.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `topic_id` | id | A row of `topics.jsonl`. |
| `author_id`? | id | A row of `users.jsonl`, or `null`. |
| `author_name` | label | The name to show. |
| `author_ip`? | ip | |
| `created_at` | time | |
| `subject` | label | |
| `body` | body | Holds `source`, `markdown`, `smilies` and `autolink`. |
| `show_signature` | bool | |
| `visibility` | string | `approved`, `unapproved` or `deleted`. |
| `edit_count` | count | |
| `edited_at`? | time | |
| `edited_by`? | id | A row of `users.jsonl`. |
| `edit_reason` | label | |

```json
{"id":1,"topic_id":1,"author_id":1,"author_name":"alice","author_ip":"10.0.0.5","created_at":"2019-05-02T09:00:00Z","subject":"Welcome","body":{"source":"Welcome to the **example** board.","markdown":true,"smilies":true,"autolink":true},"show_signature":true,"visibility":"approved","edit_count":1,"edited_at":"2019-05-02T09:05:00Z","edited_by":1,"edit_reason":"typo"}
```

## polls.jsonl

| Field | Type | Rule |
|---|---|---|
| `topic_id` | id | A row of `topics.jsonl`. Unique: a topic has at most one poll. |
| `question` | label | |
| `max_options` | int | 1 to the number of options. How many options a voter may pick. |
| `ends_at`? | time | |
| `can_change_vote` | bool | |
| `options` | list | 1 to 100 items. Each has `id`, `text` and `votes`. |

An option `id` is 1 or more and is unique in its poll. `text` is a label. `votes` is a
count: the number of votes the option has.

```json
{"topic_id":1,"question":"Which color?","max_options":1,"ends_at":null,"can_change_vote":true,"options":[{"id":1,"text":"Red","votes":1},{"id":2,"text":"Blue","votes":0}]}
```

## poll_votes.jsonl

| Field | Type | Rule |
|---|---|---|
| `topic_id` | id | A row of `polls.jsonl`. |
| `option_id` | id | An option of that poll. |
| `user_id` | id | A row of `users.jsonl`. |
| `voter_ip`? | ip | |

The triple (`topic_id`, `option_id`, `user_id`) is unique. An option has no more vote
rows than its `votes`. A user has no more vote rows in a poll than its `max_options`.

```json
{"topic_id":1,"option_id":1,"user_id":1,"voter_ip":"10.0.0.5"}
```

## attachments.jsonl

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `post_id`? | id | A row of `posts.jsonl`. `null` for an upload that no post uses. |
| `position` | count | The order of the attachments of one post. |
| `uploader_id` | id | A row of `users.jsonl`. |
| `name` | label | The file name that members see. |
| `file` | file | A file under `attachments/`. It holds `path`, `size` and `sha256`. |
| `mime` | string | `image/png`, `image/jpeg`, `image/gif` or `image/webp`. |
| `width`? | int | 1 or more. |
| `height`? | int | 1 or more. |
| `download_count` | count | |
| `comment` | string | |
| `created_at` | time | |

```json
{"id":1,"post_id":1,"position":0,"uploader_id":1,"name":"photo.png","file":{"path":"attachments/1.png","size":1,"sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"},"mime":"image/png","width":640,"height":480,"download_count":3,"comment":"","created_at":"2019-05-02T09:00:00Z"}
```

## pms.jsonl

One row is one private message with all its recipients.

| Field | Type | Rule |
|---|---|---|
| `id` | id | |
| `author_id` | id | A row of `users.jsonl`. |
| `author_ip`? | ip | |
| `created_at` | time | |
| `subject` | label | |
| `body` | body | Holds `source`, `markdown`, `smilies` and `autolink`. |
| `show_signature` | bool | |
| `author_deleted` | bool | Whether the author deleted their copy. |
| `recipients` | list | 1 or more items. Each has `user_id`, `kind`, `read` and `deleted`. |

In a recipient, `user_id` is a row of `users.jsonl`, `kind` is `to` or `bcc`, and `read`
and `deleted` are bools. A user appears once per message.

```json
{"id":1,"author_id":1,"author_ip":null,"created_at":"2025-12-01T18:00:00Z","subject":"Hello","body":{"source":"Hi Bob.","markdown":true,"smilies":true,"autolink":true},"show_signature":false,"author_deleted":false,"recipients":[{"user_id":2,"kind":"to","read":true,"deleted":false}]}
```

## topic_watches.jsonl

| Field | Type | Rule |
|---|---|---|
| `user_id` | id | A row of `users.jsonl`. |
| `topic_id` | id | A row of `topics.jsonl`. |
| `notify_pending` | bool | Whether a notification is pending. |

The pair (`user_id`, `topic_id`) is unique.

```json
{"user_id":2,"topic_id":1,"notify_pending":false}
```

## bookmarks.jsonl

| Field | Type | Rule |
|---|---|---|
| `user_id` | id | A row of `users.jsonl`. |
| `topic_id` | id | A row of `topics.jsonl`. |

The pair (`user_id`, `topic_id`) is unique.

```json
{"user_id":2,"topic_id":1}
```

## topic_marks.jsonl

A row says when a user last read a topic.

| Field | Type | Rule |
|---|---|---|
| `user_id` | id | A row of `users.jsonl`. |
| `topic_id` | id | A row of `topics.jsonl`. |
| `marked_at` | time | |

The pair (`user_id`, `topic_id`) is unique.

```json
{"user_id":2,"topic_id":1,"marked_at":"2026-01-10T08:30:00Z"}
```

## forum_marks.jsonl

A row says when a user marked a forum read.

| Field | Type | Rule |
|---|---|---|
| `user_id` | id | A row of `users.jsonl`. |
| `forum_id` | id | A row of `forums.jsonl`. |
| `marked_at` | time | |

The pair (`user_id`, `forum_id`) is unique.

```json
{"user_id":2,"forum_id":2,"marked_at":"2026-01-10T08:30:00Z"}
```

## bans.jsonl

| Field | Type | Rule |
|---|---|---|
| `kind` | string | `user`, `email` or `ip`. |
| `user_id`? | id | A row of `users.jsonl`. Set when `kind` is `user`. |
| `email`? | label | Set when `kind` is `email`. |
| `ip`? | string | Set when `kind` is `ip`. A CIDR prefix with no host bits set, for example `10.0.0.0/8` or `2001:db8::/32`. |
| `reason` | label | The reason that staff see. |
| `reason_shown` | label | The reason that the banned person sees. |
| `created_at` | time | |
| `ends_at`? | time | `null` for a ban with no end. |

Exactly the field that matches `kind` is set. The other two are `null`.

```json
{"kind":"ip","user_id":null,"email":null,"ip":"10.0.0.0/8","reason":"Spam run","reason_shown":"You are banned.","created_at":"2025-11-01T00:00:00Z","ends_at":null}
```

## smilies.jsonl

| Field | Type | Rule |
|---|---|---|
| `code` | string | The text that turns into the image, for example `:)`. 1 to 32 characters, with no control character. It is unique in the file. |
| `image` | file | An image under `smilies/`. It holds `path`, `size` and `sha256`. |
| `description` | label | The alt text of the image. |
| `position` | int | |

```json
{"code":":)","image":{"path":"smilies/smile.gif","size":1,"sha256":"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db02258717921a4881"},"description":"smile","position":1}
```

## legacy_routes.jsonl

A row maps an old URL of the source system to a gobb page.

| Field | Type | Rule |
|---|---|---|
| `path` | string | 1 to 255 bytes. No leading `/`, no `?` and no `#`. |
| `match` | object | Up to 10 pairs. Each key is a param, and each value is a label. |
| `id_param`? | param | The query parameter that holds the ID. |
| `target_kind` | string | `home`, `forum`, `topic`, `post`, `user`, `members`, `team`, `attachment`, `avatar`, `search`, `account` or `page`. |
| `target_id`? | id | A fixed ID. |
| `target_slug`? | slug | The slug of a static page. |
| `offset_param`? | param | The query parameter that holds the offset into a list. |
| `position` | int | |

The kinds `forum`, `topic`, `post`, `user`, `attachment` and `avatar` need exactly one
of `id_param` and `target_id`. The kind `page` needs `target_slug`, which names a row of
`static_pages.jsonl`, and nothing else. The other kinds need neither `id_param` nor
`target_id`. Only `forum` and `topic` may set `offset_param`. A fixed `target_id`
names a row that exists: a forum, a topic, a post, a user or an attachment, by kind. For
`avatar` it names a user. The pair (`path`, `match`) is unique.

```json
{"path":"old/thread.cgi","match":{"view":"full"},"id_param":"id","target_kind":"topic","target_id":null,"target_slug":null,"offset_param":"page","position":1}
```

## legacy_files.jsonl

A row maps an old URL prefix of the source system to a gobb directory.

| Field | Type | Rule |
|---|---|---|
| `prefix` | string | 1 to 255 bytes. No leading `/`. Ends with `/`. Unique in the file. |
| `dir` | string | `smilies` or `ranks`. |

```json
{"prefix":"img/emoticons/","dir":"smilies"}
```

## static_pages.jsonl

| Field | Type | Rule |
|---|---|---|
| `slug` | slug | Unique in the file. |
| `title` | name | |
| `body` | body | Holds `source`, `markdown`, `smilies` and `autolink`. |
| `updated_at` | time | |

```json
{"slug":"about","title":"About","body":{"source":"This is an example board.","markdown":true,"smilies":false,"autolink":true},"updated_at":"2025-06-01T12:00:00Z"}
```
