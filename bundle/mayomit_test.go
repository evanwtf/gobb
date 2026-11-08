package bundle

import (
	"reflect"
	"testing"
	"time"
)

// Only PM.ConversationID may have the tag bundle:"may-omit", and only on a
// pointer. Any other field that a row leaves out stays an error.
func TestMayOmitAllowlist(t *testing.T) {
	allowed := map[string]bool{"PM.ConversationID": true}
	seen := map[reflect.Type]bool{}
	var walk func(rt reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt == reflect.TypeFor[time.Time]() || seen[rt] {
			return
		}
		seen[rt] = true
		for i := range rt.NumField() {
			f := rt.Field(i)
			name := rt.Name() + "." + f.Name
			if tag, ok := f.Tag.Lookup("bundle"); ok {
				switch {
				case tag != "may-omit":
					t.Errorf("%s has the unknown tag bundle:%q", name, tag)
				case !allowed[name]:
					t.Errorf("%s has bundle:\"may-omit\"; only %v may", name, allowed)
				case f.Type.Kind() != reflect.Pointer:
					t.Errorf("%s has bundle:\"may-omit\" but is not a pointer", name)
				}
			}
			walk(f.Type)
		}
	}
	rows := fullRows()
	if len(rows) != len(Files) {
		t.Fatalf("fullRows has %d types, want one for each of the %d data files", len(rows), len(Files))
	}
	for _, r := range rows {
		walk(reflect.TypeOf(r))
	}
	walk(reflect.TypeFor[Manifest]())
	walk(reflect.TypeFor[Settings]())
	if !seen[reflect.TypeFor[PM]()] {
		t.Fatal("the walk did not reach PM")
	}
	f, _ := reflect.TypeFor[PM]().FieldByName("ConversationID")
	if f.Tag.Get("bundle") != "may-omit" {
		t.Error("PM.ConversationID lost its may-omit tag")
	}
}
