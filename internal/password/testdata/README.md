# Legacy password vectors

`legacy-vectors.json` holds test vectors for the check of old password hashes.

The passwords are made up. The owner's private tooling generated the file once. That
tooling runs the original system, the board that the old hashes come from. The `want`
field is the verdict of that system. Nobody wrote it by hand, and the new code did not
produce it.

## Format

The file is a JSON array of 120 objects. Each object has four fields.

| Field | Meaning |
|---|---|
| `name` | A short label for the case, such as `bcrypt-ampersand` or `chain-md5-nfd-negative`. Each name is unique. |
| `raw_b64` | The password as the user typed it, in standard base64. These are the raw bytes, before any change to the text. |
| `hash` | The stored hash. |
| `want` | `true` if the original system accepts `raw_b64` for `hash`. `false` if it rejects it. |

The original system changes the text of a password before it hashes it, and again before
it checks it. The raw bytes in `raw_b64` go through that same change before the check.

## Hash kinds

| Name starts with | Hash |
|---|---|
| `bcrypt-` | bcrypt, `$2y$10$...` |
| `argon2id-` | Argon2id, `$argon2id$v=19$m=65536,t=4,p=2$...` |
| `chain-` | `$chain$phpass,` + 9-char setting + `$2y$10$` + 22-char bcrypt salt + 31-char bcrypt digest (83 chars). The bcrypt input is the 22-char portable-hash digest of the password. |
| `chain-md5-` | The same chain format. The portable hash covers the lowercase hex MD5 of the password. |

## Cases

Each kind has 15 passwords. They test: plain words, `&`, `"`, `<` and `>`, an
apostrophe, spaces at both ends, a CR LF line break, accents in two Unicode forms (NFC
and NFD), non-Latin text, a byte order mark, text that looks like an entity
(`amp&amp;`), and passwords of 72 and 100 bytes.

Each password has a negative twin. The twin has the same `hash`. Its password has a `!`
in front, and its name ends with `-negative`. For all 60 negative twins, `want` is
`false`. For all 60 other entries, `want` is `true`.

A `!` goes in front and not at the end. Bcrypt reads only the first 72 bytes of a
password, so a `!` at the end of a 72-byte password would not change the result.

## Count

| Kind | `want` true | `want` false |
|---|---|---|
| bcrypt | 15 | 15 |
| argon2id | 15 | 15 |
| chain | 15 | 15 |
| chain-md5 | 15 | 15 |
