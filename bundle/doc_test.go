package bundle

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const docPath = "../docs/bundle.md"

// sections splits the document at its "## " headings. The key is the heading
// text without the "## ".
func sections(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	var name string
	var buf strings.Builder
	flush := func() {
		if name != "" {
			out[name] = buf.String()
		}
		buf.Reset()
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			name = strings.TrimSpace(h)
			continue
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}
	flush()
	return out
}

// jsonNames returns the JSON field names of struct type t, including the names
// in nested structs, slices of structs and pointers to structs.
func jsonNames(t reflect.Type) []string {
	var out []string
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, name)
		ft := f.Type
		for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
			ft = ft.Elem()
		}
		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Time]() {
			out = append(out, jsonNames(ft)...)
		}
	}
	return out
}

// firstJSON returns the first ```json block of a section.
func firstJSON(sec string) (string, bool) {
	_, rest, ok := strings.Cut(sec, "```json\n")
	if !ok {
		return "", false
	}
	block, _, ok := strings.Cut(rest, "```")
	return strings.TrimSpace(block), ok
}

type docCase struct {
	section string
	value   any // a pointer to a zero value of the type
}

func docCases() []docCase {
	var out []docCase
	for _, r := range fullRows() {
		out = append(out, docCase{r.DataFile() + ".jsonl", reflect.New(reflect.TypeOf(r)).Interface()})
	}
	out = append(out,
		docCase{"manifest.json", &Manifest{}},
		docCase{"settings.json", &Settings{}},
	)
	return out
}

func TestDocCoversEveryField(t *testing.T) {
	secs := sections(t)
	cases := docCases()
	if len(cases) != len(Files)+2 {
		t.Fatalf("%d cases", len(cases))
	}
	for i, f := range Files {
		if cases[i].section != f+".jsonl" {
			t.Fatalf("case %d is %q, want %q", i, cases[i].section, f+".jsonl")
		}
	}
	for _, c := range cases {
		t.Run(c.section, func(t *testing.T) {
			sec, ok := secs[c.section]
			if !ok {
				t.Fatalf("docs/bundle.md has no %q heading", "## "+c.section)
			}
			for _, name := range jsonNames(reflect.TypeOf(c.value).Elem()) {
				if !strings.Contains(sec, "`"+name+"`") {
					t.Errorf("section does not mention field `%s`", name)
				}
			}
		})
	}
}

func TestDocHeadingsInLoadOrder(t *testing.T) {
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if h, ok := strings.CutPrefix(line, "## "); ok && strings.HasSuffix(h, ".jsonl") {
			got = append(got, strings.TrimSuffix(h, ".jsonl"))
		}
	}
	if !slices.Equal(got, Files) {
		t.Errorf("data-file sections are %v, want %v", got, Files)
	}
}

// TestDocExamples decodes the first JSON example of each section with the
// strict rules of the format and runs the field checks on it.
func TestDocExamples(t *testing.T) {
	secs := sections(t)
	for _, c := range docCases() {
		t.Run(c.section, func(t *testing.T) {
			ex, ok := firstJSON(secs[c.section])
			if !ok {
				t.Fatal("section has no ```json example")
			}
			if strings.Contains(ex, "\n") && strings.HasSuffix(c.section, ".jsonl") {
				t.Error("a data-file example is one line")
			}
			dec := json.NewDecoder(strings.NewReader(ex))
			dec.DisallowUnknownFields()
			if err := dec.Decode(c.value); err != nil {
				t.Fatalf("decode: %v", err)
			}
			// Every top-level field is present, even when null.
			var m map[string]json.RawMessage
			if err := json.Unmarshal([]byte(ex), &m); err != nil {
				t.Fatal(err)
			}
			rt := reflect.TypeOf(c.value).Elem()
			for i := range rt.NumField() {
				name, _, _ := strings.Cut(rt.Field(i).Tag.Get("json"), ",")
				if _, ok := m[name]; !ok {
					t.Errorf("example lacks field %q", name)
				}
			}
			ch, ok := c.value.(checker)
			if !ok {
				t.Fatalf("%T has no check method", c.value)
			}
			for _, e := range ch.check() {
				t.Errorf("example fails its check: %v", e)
			}
			if bytes.Contains([]byte(ex), []byte("\t")) {
				t.Error("example holds a tab")
			}
		})
	}
}
