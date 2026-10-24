package postbody_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/evanwtf/gobb/internal/postbody"
	"github.com/evanwtf/gobb/markup"
)

func TestColumns(t *testing.T) {
	tree := markup.Parse("Hello **world**\n\nsecond", markup.Options{Markdown: true, Autolink: true})
	data, text, err := postbody.Columns(tree)
	if err != nil {
		t.Fatal(err)
	}
	back, err := markup.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, tree) {
		t.Errorf("the JSON does not decode to the tree:\n%s", data)
	}
	if !json.Valid(data) {
		t.Error("the JSON is not valid")
	}
	if want := markup.Render(tree, markup.RenderContext{}).Text; text != want || text == "" {
		t.Errorf("text = %q, want %q", text, want)
	}
}
