package bbcode

import (
	"encoding/json"
	"fmt"
)

// Kind tells the three kinds of node apart. It marshals as its number.
type Kind uint8

const (
	// Root is the single top node of a tree. It has no name, attributes or text.
	Root Kind = iota
	// Text is a leaf that holds literal text. It has no name, attributes or children.
	Text
	// Element is a named node with attributes and children.
	Element
)

// Nesting caps. An element's depth is the number of elements on the path from
// the root down to it, itself included. The root has depth 0.
const (
	// MaxDepth is the deepest element nesting that [Validate] accepts.
	MaxDepth = 32
	// MaxQuoteDepth is the most quote elements that [Validate] accepts on one path.
	MaxQuoteDepth = 10
)

// Node is one node of a document tree.
//
// In its canonical form, Attrs and Children are nil when empty. The JSON
// encoding omits empty fields, so a canonical tree survives a JSON round trip
// with reflect.DeepEqual. The encoder writes a non-nil empty map or slice the
// same way as a nil one. [Decode] turns an explicit empty map or slice in the
// input into nil, so the two directions agree. A tree that holds an empty map
// or slice is valid, but it does not compare equal to its decoded copy.
type Node struct {
	Kind     Kind              `json:"k"`
	Name     string            `json:"n,omitempty"`
	Attrs    map[string]string `json:"a,omitempty"`
	Text     string            `json:"t,omitempty"`
	Children []*Node           `json:"c,omitempty"`
}

// Decode parses the JSON form of a tree and puts it in canonical form. It does
// not check the schema: call [Validate] on the result before you use it.
func Decode(data []byte) (*Node, error) {
	var n Node
	if err := json.Unmarshal(data, &n); err != nil {
		return nil, fmt.Errorf("bbcode: decode tree: %w", err)
	}
	canonicalize(&n)
	return &n, nil
}

// canonicalize sets every empty Attrs map and Children slice to nil. It uses
// an explicit stack. The input comes from JSON, so it cannot hold a cycle.
func canonicalize(root *Node) {
	stack := []*Node{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil {
			continue
		}
		if len(n.Attrs) == 0 {
			n.Attrs = nil
		}
		if len(n.Children) == 0 {
			n.Children = nil
		}
		stack = append(stack, n.Children...)
	}
}
