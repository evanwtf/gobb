package bundle

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// expect reads every row of type T and checks that they equal want, in order,
// with line numbers from 1.
func expect[T Row](t *testing.T, r *Reader, want ...T) {
	t.Helper()
	var got []T
	var lines []int
	err := Each(r, func(line int, row T) error {
		got = append(got, row)
		lines = append(lines, line)
		return nil
	})
	if err != nil {
		t.Errorf("%s: %v", want[0].DataFile(), err)
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: got %+v, want %+v", want[0].DataFile(), got, want)
	}
	for i, l := range lines {
		if l != i+1 {
			t.Errorf("%s: row %d has line %d", want[0].DataFile(), i, l)
		}
	}
}

func TestWriteThenRead(t *testing.T) {
	src := filepath.Join(t.TempDir(), "rank.png")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bundle")
	settings := validSettings()
	w, err := Create(dir, "example", settings)
	if err != nil {
		t.Fatal(err)
	}
	fr, err := w.AddFile(src, "ranks/1.png")
	if err != nil {
		t.Fatal(err)
	}
	rows := validRows()
	rows[0] = Rank{ID: 1, Title: "Regular", MinPosts: 10, Image: &fr}
	second := validUser()
	second.ID = 2
	second.Username = "bob"
	second.LastVisitAt = &testTime
	rows = append(rows, second) // a second users row, to test order
	for _, row := range rows {
		switch r := row.(type) {
		case Rank:
			err = Add(w, r)
		case User:
			err = Add(w, r)
		case GoneUser:
			err = Add(w, r)
		case Group:
			err = Add(w, r)
		case GroupMember:
			err = Add(w, r)
		case Role:
			err = Add(w, r)
		case Grant:
			err = Add(w, r)
		case Forum:
			err = Add(w, r)
		case Topic:
			err = Add(w, r)
		case Post:
			err = Add(w, r)
		case Poll:
			err = Add(w, r)
		case PollVote:
			err = Add(w, r)
		case Attachment:
			err = Add(w, r)
		case PM:
			err = Add(w, r)
		case TopicMark:
			err = Add(w, r)
		case ForumMark:
			err = Add(w, r)
		case Ban:
			err = Add(w, r)
		case Smiley:
			err = Add(w, r)
		case LegacyRoute:
			err = Add(w, r)
		case LegacyFile:
			err = Add(w, r)
		case StaticPage:
			err = Add(w, r)
		default:
			t.Fatalf("unhandled %T", row)
		}
		if err != nil {
			t.Fatalf("Add %T: %v", row, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir() != dir {
		t.Errorf("Dir() = %q, want %q", r.Dir(), dir)
	}
	if !reflect.DeepEqual(r.Settings(), settings) {
		t.Errorf("Settings() = %+v, want %+v", r.Settings(), settings)
	}
	m := r.Manifest()
	if m.Format != Format || m.Version != Version || m.Source != "example" {
		t.Errorf("manifest = %+v", m)
	}
	if m.CreatedAt.IsZero() || m.CreatedAt.Location().String() != "UTC" {
		t.Errorf("created_at = %v; want a UTC time", m.CreatedAt)
	}
	if fr.Size != 5 || fr.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" {
		t.Errorf("AddFile ref = %+v", fr)
	}
	data, err := os.ReadFile(filepath.Join(dir, "files", "ranks", "1.png"))
	if err != nil || string(data) != "hello" {
		t.Errorf("file = %q, %v", data, err)
	}

	for _, f := range Files {
		want := 1
		if f == "users" {
			want = 2
		}
		if m.Counts[f] != want {
			t.Errorf("counts[%s] = %d, want %d", f, m.Counts[f], want)
		}
	}
	expect(t, r, rows[0].(Rank))
	expect(t, r, rows[1].(User), second)
	expect(t, r, rows[2].(GoneUser))
	expect(t, r, rows[3].(Group))
	expect(t, r, rows[4].(GroupMember))
	expect(t, r, rows[5].(Role))
	expect(t, r, rows[6].(Grant))
	expect(t, r, rows[7].(Forum))
	expect(t, r, rows[8].(Topic))
	expect(t, r, rows[9].(Post))
	expect(t, r, rows[10].(Poll))
	expect(t, r, rows[11].(PollVote))
	expect(t, r, rows[12].(Attachment))
	expect(t, r, rows[13].(PM))
	expect(t, r, rows[14].(TopicMark))
	expect(t, r, rows[15].(ForumMark))
	expect(t, r, rows[16].(Ban))
	expect(t, r, rows[17].(Smiley))
	expect(t, r, rows[18].(LegacyRoute))
	expect(t, r, rows[19].(LegacyFile))
	expect(t, r, rows[20].(StaticPage))
}

func TestWriteThenReadHeaderImage(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	s := validSettings()
	s.HeaderImage = ptr(ref("header"))
	w, err := Create(dir, "example", s)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Settings(), s) {
		t.Errorf("Settings() = %+v, want %+v", r.Settings(), s)
	}
	// The reader hands out copies.
	r.Manifest().Counts["posts"] = 99
	r.Settings().HeaderImage.Size = 99
	if r.Manifest().Counts["posts"] != 0 || r.Settings().HeaderImage.Size != 12 {
		t.Error("a caller changed the reader's own data")
	}
}

func TestOpenRefuses(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, dir string)
		want string
	}{
		{"wrong format", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { m["format"] = "other" })
		}, `manifest.json: format: is "other"`},
		{"version 3", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { m["version"] = 3 })
		}, "manifest.json: version: is 3"},
		{"extra top-level file", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "notes.txt"), "x")
		}, "notes.txt: is not part of the bundle layout"},
		{"AppleDouble file", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "._users.jsonl"), "x")
		}, "._users.jsonl: is not part of the bundle layout"},
		{"extra directory", func(t *testing.T, dir string) {
			if err := os.Mkdir(filepath.Join(dir, "other"), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "other: is not part of the bundle layout"},
		{"missing data file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "users.jsonl")); err != nil {
				t.Fatal(err)
			}
		}, "users.jsonl: is missing"},
		{"missing manifest", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "manifest.json")); err != nil {
				t.Fatal(err)
			}
		}, "manifest.json: is missing"},
		{"files is a file", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "files")); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, "files"), "x")
		}, "files: must be a directory"},
		{"data file is a directory", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "users.jsonl")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		}, "users.jsonl: must be a regular file"},
		{"data file is a symlink", func(t *testing.T, dir string) {
			p := filepath.Join(dir, "users.jsonl")
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("posts.jsonl", p); err != nil {
				t.Skip("no symlinks:", err)
			}
		}, "users.jsonl: must be a regular file"},
		{"counts key missing", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) {
				delete(m["counts"].(map[string]any), "posts")
			})
		}, `manifest.json: counts: has no key "posts"`},
		{"counts key extra", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) {
				m["counts"].(map[string]any)["extra"] = 1
			})
		}, `manifest.json: counts: has the unknown key "extra"`},
		{"unknown field in settings", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) { m["theme"] = "x" })
		}, "settings.json: theme: unknown field"},
		{"unknown field in manifest", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { m["extra"] = 1 })
		}, "manifest.json: extra: unknown field"},
		{"missing key in manifest", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { delete(m, "source") })
		}, "manifest.json: source: missing"},
		{"missing key in settings", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) { delete(m, "timezone") })
		}, "settings.json: timezone: missing"},
		{"missing nullable key in settings", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) { delete(m, "header_image") })
		}, "settings.json: header_image: missing"},
		{"null in settings", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) { m["board_name"] = nil })
		}, "settings.json: board_name: must not be null"},
		{"null counts", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { m["counts"] = nil })
		}, "manifest.json: counts: must not be null"},
		{"missing nested key in header image", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) {
				m["header_image"] = map[string]any{"path": "header/a.png", "size": 1}
			})
		}, "settings.json: header_image.sha256: missing"},
		{"manifest time without offset", func(t *testing.T, dir string) {
			editJSON(t, dir, "manifest.json", func(m map[string]any) { m["created_at"] = "2026-01-02T03:04:05" })
		}, "manifest.json: created_at:"},
		{"settings is not an object", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "settings.json"), "[]")
		}, "settings.json: must be an object"},
		{"settings has trailing data", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "settings.json"), "{} {}")
		}, "settings.json: trailing data"},
		{"settings is not JSON", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "settings.json"), "{")
		}, "settings.json: invalid JSON"},
		{"settings is invalid UTF-8", func(t *testing.T, dir string) {
			writeFile(t, filepath.Join(dir, "settings.json"), "{\"board_name\":\"\xff\"}")
		}, "settings.json: invalid UTF-8"},
		{"settings value fails its check", func(t *testing.T, dir string) {
			editJSON(t, dir, "settings.json", func(m map[string]any) { m["timezone"] = "Mars/Base" })
		}, "settings.json: timezone:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newBundle(t)
			tt.edit(t, dir)
			_, err := Open(dir)
			if err == nil {
				t.Fatal("Open succeeded")
			}
			var be *Error
			if !errors.As(err, &be) {
				t.Fatalf("error is %T, want *Error: %v", err, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("not a directory", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "f")
		writeFile(t, p, "x")
		if _, err := Open(p); err == nil {
			t.Error("Open of a file succeeded")
		}
	})
	t.Run("does not exist", func(t *testing.T) {
		if _, err := Open(filepath.Join(t.TempDir(), "none")); err == nil {
			t.Error("Open of a missing directory succeeded")
		}
	})
}

func TestOpenAcceptsMissingFilesDir(t *testing.T) {
	dir := newBundle(t)
	if err := os.Remove(filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); err != nil {
		t.Errorf("Open without files/: %v", err)
	}
}

// TestOpenReadsVersionFirst pins that a manifest from a newer format reports
// its version, not the first new key.
func TestOpenReadsVersionFirst(t *testing.T) {
	tests := []struct {
		name string
		edit func(m map[string]any)
		want string
	}{
		{"new key", func(m map[string]any) { m["version"] = 3; m["new_key"] = 1 },
			"manifest.json: version: is 3; this reader knows version 2"},
		{"missing key", func(m map[string]any) { m["version"] = 3; delete(m, "source") },
			"manifest.json: version: is 3; this reader knows version 2"},
		{"changed counts", func(m map[string]any) { m["version"] = 3; m["counts"] = []any{} },
			"manifest.json: version: is 3; this reader knows version 2"},
		{"wrong format", func(m map[string]any) { m["format"] = "other"; m["new_key"] = 1 },
			`manifest.json: format: is "other"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newBundle(t)
			editJSON(t, dir, "manifest.json", tt.edit)
			_, err := Open(dir)
			if err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Errorf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

// TestVersion1Refused pins the message that tells a converter author what to do.
func TestVersion1Refused(t *testing.T) {
	const want = "manifest.json: version: bundle format version 1; this gobb reads version 2. " +
		"Build the bundle again with a converter for version 2."
	tests := []struct {
		name string
		edit func(m map[string]any)
	}{
		{"manifest of version 2 with version 1", func(m map[string]any) { m["version"] = 1 }},
		{"version 1 counts", func(m map[string]any) {
			m["version"] = 1
			m["counts"] = map[string]any{"topic_watches": 1, "bookmarks": 1}
		}},
		{"version 1 and another key", func(m map[string]any) { m["version"] = 1; m["new_key"] = 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := newBundle(t)
			editJSON(t, dir, "manifest.json", tt.edit)
			if _, err := Open(dir); err == nil || err.Error() != want {
				t.Errorf("Open = %v, want %q", err, want)
			}
		})
	}
}

// TestWatchFilesUnknown pins that version 2 has no file for watches or bookmarks.
func TestWatchFilesUnknown(t *testing.T) {
	for _, name := range []string{"topic_watches.jsonl", "bookmarks.jsonl"} {
		t.Run(name, func(t *testing.T) {
			dir := newBundle(t)
			writeFile(t, filepath.Join(dir, name), "")
			want := name + ": is not part of the bundle layout"
			if _, err := Open(dir); err == nil || err.Error() != want {
				t.Errorf("Open = %v, want %q", err, want)
			}
		})
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEachRefusesInvalidUTF8AndNUL(t *testing.T) {
	good := userRows(t, 1)
	t.Run("invalid UTF-8", func(t *testing.T) {
		bad := strings.Replace(line(t, validUser(), nil), `"alice"`, "\"al\xffice\"", 1)
		r := dataBundle(t, "users", good+bad+"\n", 2)
		if got, want := eachErr[User](t, r), "users.jsonl:2: invalid UTF-8"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
	t.Run("NUL in a username", func(t *testing.T) {
		bad := strings.Replace(line(t, validUser(), nil), `"alice"`, `"a\u0000b"`, 1)
		r := dataBundle(t, "users", good+bad+"\n", 2)
		got := eachErr[User](t, r)
		if got != "users.jsonl:2: username: holds U+0000" {
			t.Errorf("error = %q", got)
		}
	})
	t.Run("NUL in a nested string", func(t *testing.T) {
		bad := strings.Replace(line(t, validPost(), nil), `"Hello"`, `"a\u0000b"`, 1)
		r := dataBundle(t, "posts", bad+"\n", 1)
		if got, want := eachErr[Post](t, r), "posts.jsonl:1: body.source: holds U+0000"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
	t.Run("NUL in a list item", func(t *testing.T) {
		role := validRole()
		bad := strings.Replace(line(t, role, nil), `"forum.read"`, `"forum.\u0000read"`, 1)
		r := dataBundle(t, "roles", bad+"\n", 1)
		if got, want := eachErr[Role](t, r), "roles.jsonl:1: settings[0].option: holds U+0000"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
	t.Run("NUL in a map value", func(t *testing.T) {
		route := validRoute()
		route.Match = map[string]string{"mode": "x"}
		bad := strings.Replace(line(t, route, nil), `"x"`, `"x\u0000"`, 1)
		r := dataBundle(t, "legacy_routes", bad+"\n", 1)
		if got, want := eachErr[LegacyRoute](t, r), "legacy_routes.jsonl:1: match.mode: holds U+0000"; got != want {
			t.Errorf("error = %q, want %q", got, want)
		}
	})
}

func TestEachRefusesBadLines(t *testing.T) {
	good := line(t, validUser(), nil)
	goodPost := line(t, validPost(), nil)
	tests := []struct {
		name    string
		file    string
		content string
		count   int
		do      func(t *testing.T, r *Reader) string
		want    string
	}{
		{"unknown field", "users", line(t, validUser(), func(m map[string]any) { m["extra"] = 1 }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: extra: unknown field"},
		{"unknown nested field", "posts", line(t, validPost(), func(m map[string]any) {
			m["body"].(map[string]any)["html"] = "x"
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			"posts.jsonl:1: body.html: unknown field"},
		{"key in the wrong case", "users", strings.Replace(good, `"id":`, `"ID":`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: id: missing"},
		{"duplicate key", "users", strings.Replace(good, `"id":1`, `"id":1,"id":2`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: id: appears twice"},
		{"line over 1 MiB", "users", good + strings.Repeat(" ", MaxLineBytes) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: line is longer than 1048576 bytes"},
		{"line over 1 MiB, no newline", "users", good + "\n" + good + strings.Repeat(" ", MaxLineBytes), 2,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:2: line is longer than 1048576 bytes"},
		{"CRLF line ending", "users", good + "\r\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: CRLF line ending; use LF"},
		{"CRLF on line 2", "users", good + "\n" + good + "\r\n", 2,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:2: CRLF line ending; use LF"},
		{"CR alone", "users", good + "\n\r\n", 2,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:2: CRLF line ending; use LF"},
		{"lone high surrogate", "users", strings.Replace(good, `"alice"`, `"a\ud800b"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			`users.jsonl:1: username: holds the lone surrogate escape \ud800`},
		{"lone low surrogate", "users", strings.Replace(good, `"alice"`, `"a\uDC00b"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			`users.jsonl:1: username: holds the lone surrogate escape \uDC00`},
		{"high surrogate at the end of a string", "users", strings.Replace(good, `"alice"`, `"a\ud83d"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			`users.jsonl:1: username: holds the lone surrogate escape \ud83d`},
		{"high surrogate then a non-low escape", "users", strings.Replace(good, `"alice"`, `"a\ud83d\u0041"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			`users.jsonl:1: username: holds the lone surrogate escape \ud83d`},
		{"surrogate in a nested string", "posts", strings.Replace(goodPost, `"Hello"`, `"\udfff"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			`posts.jsonl:1: body.source: holds the lone surrogate escape \udfff`},
		{"surrogate in a map key", "legacy_routes", strings.Replace(line(t, func() LegacyRoute {
			r := validRoute()
			r.Match = map[string]string{"mode": "x"}
			return r
		}(), nil), `"mode"`, `"mo\ud800"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[LegacyRoute](t, r) },
			`legacy_routes.jsonl:1: holds the lone surrogate escape \ud800`},
		{"NUL in a map key", "legacy_routes", strings.Replace(line(t, func() LegacyRoute {
			r := validRoute()
			r.Match = map[string]string{"mode": "x"}
			return r
		}(), nil), `"mode"`, `"mo\u0000de"`, 1) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[LegacyRoute](t, r) },
			`legacy_routes.jsonl:1: match: key "mo\x00de" holds U+0000`},
		{"empty line", "users", good + "\n\n" + good + "\n", 3,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:2: empty line"},
		{"blank line", "users", good + "\n  \t\n", 2,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:2: empty line"},
		{"two values", "users", good + good + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: trailing data after the JSON value"},
		{"junk after value", "users", good + " x\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: trailing data after the JSON value"},
		{"not JSON", "users", "{\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: invalid JSON"},
		{"not an object", "users", "[]\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: must be an object"},
		{"bad enum", "users", line(t, validUser(), func(m map[string]any) { m["state"] = "banned" }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			`users.jsonl:1: state: is "banned"`},
		{"wrong type", "users", line(t, validUser(), func(m map[string]any) { m["post_count"] = "3" }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: post_count: must be an integer"},
		{"fraction for an integer", "users", line(t, validUser(), func(m map[string]any) { m["post_count"] = 1.5 }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: post_count: must be an integer"},
		{"wrong type in a list item", "polls", line(t, validPoll(), func(m map[string]any) {
			m["options"].([]any)[1].(map[string]any)["votes"] = "x"
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Poll](t, r) },
			"polls.jsonl:1: options[1].votes: must be an integer"},
		{"time without an offset", "users", line(t, validUser(), func(m map[string]any) {
			m["registered_at"] = "2026-01-02T03:04:05"
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: registered_at: must be an RFC 3339 time with an offset"},
		{"nullable time without an offset", "users", line(t, validUser(), func(m map[string]any) {
			m["last_visit_at"] = "2026-01-02 03:04:05Z"
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: last_visit_at: must be an RFC 3339 time with an offset"},
		{"missing key", "users", line(t, validUser(), func(m map[string]any) { delete(m, "owner") }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: owner: missing"},
		{"missing nullable key", "users", line(t, validUser(), func(m map[string]any) { delete(m, "rank_id") }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: rank_id: missing"},
		{"missing nested key, line 4", "posts", strings.Repeat(goodPost+"\n", 3) + line(t, validPost(), func(m map[string]any) {
			delete(m["body"].(map[string]any), "markdown")
		}) + "\n", 4,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			"posts.jsonl:4: body.markdown: missing"},
		{"missing key in password", "users", line(t, validUser(), func(m map[string]any) {
			delete(m["password"].(map[string]any), "hash")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: password.hash: missing"},
		{"missing key in signature", "users", line(t, validUser(), func(m map[string]any) {
			delete(m["signature"].(map[string]any), "source")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: signature.source: missing"},
		{"missing key in file", "attachments", line(t, validAttachment(), func(m map[string]any) {
			delete(m["file"].(map[string]any), "sha256")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Attachment](t, r) },
			"attachments.jsonl:1: file.sha256: missing"},
		{"missing key in a role setting", "roles", line(t, validRole(), func(m map[string]any) {
			delete(m["settings"].([]any)[0].(map[string]any), "setting")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Role](t, r) },
			"roles.jsonl:1: settings[0].setting: missing"},
		{"missing key in a poll option", "polls", line(t, validPoll(), func(m map[string]any) {
			delete(m["options"].([]any)[1].(map[string]any), "votes")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Poll](t, r) },
			"polls.jsonl:1: options[1].votes: missing"},
		{"missing key in a recipient", "pms", line(t, validPM(), func(m map[string]any) {
			delete(m["recipients"].([]any)[1].(map[string]any), "deleted")
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[PM](t, r) },
			"pms.jsonl:1: recipients[1].deleted: missing"},
		{"missing nested key in an optional file", "users", line(t, validUser(), func(m map[string]any) {
			m["avatar"] = map[string]any{"path": "avatars/a.png", "size": 1}
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: avatar.sha256: missing"},
		{"null in a bool", "users", line(t, validUser(), func(m map[string]any) { m["owner"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: owner: must not be null"},
		{"null in a string", "users", line(t, validUser(), func(m map[string]any) { m["email"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: email: must not be null"},
		{"null in a number", "users", line(t, validUser(), func(m map[string]any) { m["id"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: id: must not be null"},
		{"null in a time", "users", line(t, validUser(), func(m map[string]any) { m["registered_at"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: registered_at: must not be null"},
		{"null in an object", "users", line(t, validUser(), func(m map[string]any) { m["signature"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: signature: must not be null"},
		{"null in a nested bool", "posts", line(t, validPost(), func(m map[string]any) {
			m["body"].(map[string]any)["smilies"] = nil
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			"posts.jsonl:1: body.smilies: must not be null"},
		{"null settings list", "roles", line(t, validRole(), func(m map[string]any) { m["settings"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Role](t, r) },
			"roles.jsonl:1: settings: must not be null"},
		{"null options list", "polls", line(t, validPoll(), func(m map[string]any) { m["options"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Poll](t, r) },
			"polls.jsonl:1: options: must not be null"},
		{"null recipients list", "pms", line(t, validPM(), func(m map[string]any) { m["recipients"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[PM](t, r) },
			"pms.jsonl:1: recipients: must not be null"},
		{"null match", "legacy_routes", line(t, validRoute(), func(m map[string]any) { m["match"] = nil }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[LegacyRoute](t, r) },
			"legacy_routes.jsonl:1: match: must not be null"},
		{"null in a list item", "roles", line(t, validRole(), func(m map[string]any) {
			m["settings"] = []any{nil}
		}) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Role](t, r) },
			"roles.jsonl:1: settings[0]: must not be null"},
		{"list is not a list", "roles", line(t, validRole(), func(m map[string]any) { m["settings"] = "x" }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[Role](t, r) },
			"roles.jsonl:1: settings: must be a list"},
		{"object is not an object", "users", line(t, validUser(), func(m map[string]any) { m["signature"] = "x" }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: signature: must be an object"},
		{"field check fails", "users", line(t, validUser(), func(m map[string]any) { m["post_count"] = 1 << 31 }) + "\n", 1,
			func(t *testing.T, r *Reader) string { return eachErr[User](t, r) },
			"users.jsonl:1: post_count: is 2147483648; the limit is 2147483647"},
		{"count differs", "posts", strings.Repeat(goodPost+"\n", 3), 4,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			"posts.jsonl: 3 rows, manifest says 4"},
		{"count too small", "posts", strings.Repeat(goodPost+"\n", 3), 2,
			func(t *testing.T, r *Reader) string { return eachErr[Post](t, r) },
			"posts.jsonl: 3 rows, manifest says 2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := dataBundle(t, tt.file, tt.content, tt.count)
			if got := tt.do(t, r); !strings.HasPrefix(got, tt.want) {
				t.Errorf("error = %q\nwant prefix %q", got, tt.want)
			}
		})
	}
}

func TestEachAcceptsEdges(t *testing.T) {
	good := line(t, validUser(), nil)
	t.Run("null in nullable fields", func(t *testing.T) {
		l := line(t, validUser(), func(m map[string]any) {
			m["last_visit_at"], m["rank_id"], m["avatar"] = nil, nil, nil
		})
		r := dataBundle(t, "users", l+"\n", 1)
		expect(t, r, validUser())
	})
	t.Run("a valid surrogate pair", func(t *testing.T) {
		u := validUser()
		u.Username = "a\U0001F600b"
		l := strings.Replace(line(t, u, nil), "\U0001F600", `\ud83d\ude00`, 1)
		if !strings.Contains(l, `\ud83d\ude00`) {
			t.Fatal("the test line has no escape")
		}
		r := dataBundle(t, "users", l+"\n", 1)
		expect(t, r, u)
	})
	t.Run("an escaped backslash before u", func(t *testing.T) {
		u := validUser()
		u.Username = `a\ud800b` // a backslash, not an escape
		r := dataBundle(t, "users", line(t, u, nil)+"\n", 1)
		expect(t, r, u)
	})
	t.Run("no newline at the end", func(t *testing.T) {
		r := dataBundle(t, "users", good, 1)
		expect(t, r, validUser())
	})
	t.Run("line of exactly 1 MiB", func(t *testing.T) {
		pad := strings.Repeat(" ", MaxLineBytes-len(good))
		r := dataBundle(t, "users", good+pad+"\n", 1)
		expect(t, r, validUser())
	})
	t.Run("empty file with count 0", func(t *testing.T) {
		r := dataBundle(t, "users", "", 0)
		n := 0
		if err := Each(r, func(int, User) error { n++; return nil }); err != nil || n != 0 {
			t.Errorf("n = %d, err = %v", n, err)
		}
	})
	t.Run("many lines", func(t *testing.T) {
		const n = 5000
		r := dataBundle(t, "users", userRows(t, n), n)
		count := 0
		err := Each(r, func(l int, u User) error {
			count++
			if l != count || u.ID != int64(count) {
				t.Fatalf("line %d has user %d", l, u.ID)
			}
			return nil
		})
		if err != nil || count != n {
			t.Errorf("count = %d, err = %v", count, err)
		}
	})
	t.Run("a pointer type is refused", func(t *testing.T) {
		r := dataBundle(t, "users", "", 0)
		if err := Each(r, func(int, *User) error { return nil }); err == nil {
			t.Error("Each over *User succeeded")
		}
	})
}

func TestEachStopsOnCallbackError(t *testing.T) {
	r := dataBundle(t, "users", userRows(t, 3), 3)
	stop := errors.New("stop here")
	var seen []int
	err := Each(r, func(l int, _ User) error {
		seen = append(seen, l)
		if l == 2 {
			return stop
		}
		return nil
	})
	if err != stop { //nolint:errorlint // the error must come back unchanged
		t.Errorf("err = %v, want the callback's own error", err)
	}
	if !reflect.DeepEqual(seen, []int{1, 2}) {
		t.Errorf("fn saw lines %v, want [1 2]", seen)
	}
}

// TestEachBadRowDoesNotCallFn pins that a row that fails a check never reaches
// the callback.
func TestEachBadRowDoesNotCallFn(t *testing.T) {
	bad := line(t, validUser(), func(m map[string]any) { m["state"] = "banned" })
	r := dataBundle(t, "users", userRows(t, 1)+bad+"\n", 2)
	calls := 0
	err := Each(r, func(int, User) error { calls++; return nil })
	if err == nil || calls != 1 {
		t.Errorf("calls = %d, err = %v", calls, err)
	}
	var be *Error
	if !errors.As(err, &be) || be.File != "users.jsonl" || be.Line != 2 || be.Field != "state" {
		t.Errorf("err = %#v", err)
	}
}
