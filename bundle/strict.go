package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

var timeType = reflect.TypeFor[time.Time]()

// jsonField is one field of a row struct, as the JSON names it.
type jsonField struct {
	name  string
	index int
	typ   reflect.Type
}

var fieldCache sync.Map // reflect.Type -> []jsonField

// jsonFields returns the JSON fields of struct type t, in declaration order.
func jsonFields(t reflect.Type) []jsonField {
	if c, ok := fieldCache.Load(t); ok {
		return c.([]jsonField)
	}
	var out []jsonField
	for i := range t.NumField() {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" || name == "" {
			continue
		}
		out = append(out, jsonField{name: name, index: i, typ: f.Type})
	}
	fieldCache.Store(t, out)
	return out
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

func isNull(raw json.RawMessage) bool { return string(bytes.TrimSpace(raw)) == "null" }

// decodeStrict decodes one JSON object from data into a T and proves the rules
// that encoding/json does not:
//
//   - the text is valid UTF-8, and one value follows no other data;
//   - every key of every struct in T is present, and no other key is;
//   - no key appears twice;
//   - null appears only where T has a pointer;
//   - no string holds U+0000.
//
// T must be a struct type. The returned error has Field set and File and Line
// empty.
func decodeStrict[T any](data []byte) (T, *Error) {
	var out T
	if !utf8.Valid(data) {
		return out, &Error{Msg: "invalid UTF-8"}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return out, &Error{Msg: "invalid JSON: " + err.Error()}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return out, &Error{Msg: "trailing data after the JSON value"}
	}
	if e := walk(raw, reflect.TypeFor[T](), ""); e != nil {
		return out, e
	}
	// The walk proved the shape, so this decode only fills the value. The
	// strict mode stays on as a second guard.
	dd := json.NewDecoder(bytes.NewReader(raw))
	dd.DisallowUnknownFields()
	if err := dd.Decode(&out); err != nil {
		var te *json.UnmarshalTypeError
		if errors.As(err, &te) {
			return out, &Error{Field: te.Field, Msg: "cannot use JSON " + te.Value + " here"}
		}
		return out, &Error{Msg: err.Error()}
	}
	if e := findNUL(reflect.ValueOf(out), ""); e != nil {
		return out, e
	}
	return out, nil
}

// walk checks the JSON value raw against type t. It returns the first fault.
func walk(raw json.RawMessage, t reflect.Type, path string) *Error {
	raw = bytes.TrimSpace(raw)
	if t.Kind() == reflect.Pointer {
		if isNull(raw) {
			return nil
		}
		t = t.Elem()
	} else if isNull(raw) {
		return &Error{Field: path, Msg: "must not be null"}
	}
	switch {
	case t.Kind() == reflect.Struct && t != timeType:
		keys, vals, dup, err := parseObject(raw)
		if err != nil {
			return &Error{Field: path, Msg: err.Error()}
		}
		if dup != "" {
			return &Error{Field: joinPath(path, dup), Msg: "appears twice"}
		}
		fields := jsonFields(t)
		known := make(map[string]bool, len(fields))
		for _, f := range fields {
			known[f.name] = true
			v, ok := vals[f.name]
			if !ok {
				return &Error{Field: joinPath(path, f.name), Msg: "missing"}
			}
			if e := walk(v, f.typ, joinPath(path, f.name)); e != nil {
				return e
			}
		}
		for _, k := range keys {
			if !known[k] {
				return &Error{Field: joinPath(path, clipKey(k)), Msg: "unknown field"}
			}
		}
		return nil
	case t.Kind() == reflect.Slice:
		items, err := parseArray(raw)
		if err != nil {
			return &Error{Field: path, Msg: err.Error()}
		}
		for i, item := range items {
			if e := walk(item, t.Elem(), path+"["+strconv.Itoa(i)+"]"); e != nil {
				return e
			}
		}
		return nil
	case t.Kind() == reflect.Map:
		keys, vals, dup, err := parseObject(raw)
		if err != nil {
			return &Error{Field: path, Msg: err.Error()}
		}
		if dup != "" {
			return &Error{Field: joinPath(path, dup), Msg: "appears twice"}
		}
		for _, k := range keys {
			if e := walk(vals[k], t.Elem(), joinPath(path, clipKey(k))); e != nil {
				return e
			}
		}
		return nil
	}
	if err := json.Unmarshal(raw, reflect.New(t).Interface()); err != nil {
		return &Error{Field: path, Msg: leafMessage(t)}
	}
	return nil
}

// clipKey shortens a key that came from the bundle, for a message.
func clipKey(k string) string { return clip(k) }

// leafMessage says what a scalar field must be.
func leafMessage(t reflect.Type) string {
	switch {
	case t == timeType:
		return "must be an RFC 3339 time with an offset, for example 2020-03-04T05:06:07Z"
	case t.Kind() == reflect.String:
		return "must be a string"
	case t.Kind() == reflect.Bool:
		return "must be true or false"
	case t.Kind() >= reflect.Int && t.Kind() <= reflect.Int64:
		return "must be an integer"
	default:
		return "has the wrong type"
	}
}

// parseObject splits a JSON object into its keys, in order, and its values. It
// returns the first key that appears twice.
func parseObject(raw json.RawMessage) (keys []string, vals map[string]json.RawMessage, dup string, err error) {
	if len(raw) == 0 || raw[0] != '{' {
		return nil, nil, "", errors.New("must be an object")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if _, err := dec.Token(); err != nil {
		return nil, nil, "", err
	}
	vals = map[string]json.RawMessage{}
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, nil, "", err
		}
		k, ok := kt.(string)
		if !ok {
			return nil, nil, "", errors.New("must be an object")
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, "", err
		}
		if _, seen := vals[k]; seen && dup == "" {
			dup = clipKey(k)
		}
		if _, seen := vals[k]; !seen {
			keys = append(keys, k)
		}
		vals[k] = v
	}
	return keys, vals, dup, nil
}

// parseArray splits a JSON array into its items.
func parseArray(raw json.RawMessage) ([]json.RawMessage, error) {
	if len(raw) == 0 || raw[0] != '[' {
		return nil, errors.New("must be a list")
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// findNUL walks v and returns a fault for the first string that holds U+0000.
// It looks at every string: struct fields, list items, map keys and map values.
func findNUL(v reflect.Value, path string) *Error {
	switch v.Kind() {
	case reflect.String:
		if strings.IndexByte(v.String(), 0) >= 0 {
			return &Error{Field: path, Msg: "holds U+0000"}
		}
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			return findNUL(v.Elem(), path)
		}
	case reflect.Struct:
		if v.Type() == timeType {
			return nil
		}
		for _, f := range jsonFields(v.Type()) {
			if e := findNUL(v.Field(f.index), joinPath(path, f.name)); e != nil {
				return e
			}
		}
	case reflect.Slice:
		for i := range v.Len() {
			if e := findNUL(v.Index(i), path+"["+strconv.Itoa(i)+"]"); e != nil {
				return e
			}
		}
	case reflect.Map:
		for _, k := range sortedMapKeys(v) {
			if strings.IndexByte(k, 0) >= 0 {
				return &Error{Field: joinPath(path, clipKey(k)), Msg: "key holds U+0000"}
			}
			if e := findNUL(v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key())), joinPath(path, k)); e != nil {
				return e
			}
		}
	}
	return nil
}

// sortedMapKeys returns the string keys of map v in order.
func sortedMapKeys(v reflect.Value) []string {
	if v.Type().Key().Kind() != reflect.String {
		return nil
	}
	keys := make([]string, 0, v.Len())
	for _, k := range v.MapKeys() {
		keys = append(keys, k.String())
	}
	slices.Sort(keys)
	return keys
}
