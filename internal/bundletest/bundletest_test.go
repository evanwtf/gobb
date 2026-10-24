package bundletest

import (
	"bytes"
	"image"
	_ "image/gif" // register the decoders
	_ "image/png"
	"reflect"
	"testing"
)

func TestImagesDecode(t *testing.T) {
	for path, data := range Small().Files {
		if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
			t.Errorf("%s: %v", path, err)
		}
	}
}

func TestSmallIsStable(t *testing.T) {
	if !reflect.DeepEqual(Small(), Small()) {
		t.Fatal("two calls to Small differ")
	}
	a, b := Small(), Small()
	a.Users[0].Username = "changed"
	if b.Users[0].Username == "changed" {
		t.Fatal("calls to Small share state")
	}
}
