package markup

import (
	"sort"
	"strconv"
	"strings"
)

// Diff is one difference between two trees.
//
// Path names the node, one step per element, in the form that [Error] uses: the
// name of the node and its index in its parent, for example "/quote[0]/b[1]". A
// text node has the name "#text". The top node has the path "/".
//
// Want and Got describe the node in each tree:
//   - For a difference in text, each side is the text, quoted as Go quotes a
//     string.
//   - For a difference in attributes, each side is the attribute list, sorted
//     by name, in the form "k=v,k=v". An empty list is "".
//   - For a node of another kind or name, each side names its node: "root",
//     the element name, `text "abc"`, or "nil".
//   - For a child that one tree lacks, the side of that tree is "" and the other
//     side names the child in the same way.
type Diff struct {
	Path string // the node, for example "/quote[0]/b[1]"
	Want string // the node in the expected tree
	Got  string // the node in the tree that Parse made
}

// step is one link in the chain of ancestors of a node. The chain gives the
// path of a node, which Compare builds only when it reports a difference.
type step struct {
	parent int    // index of the parent's step; -1 for the top
	name   string // the name of the node, as the path writes it
	index  int    // the index of the node in its parent
}

// pair is a node of the expected tree and a node of the other tree, or only one
// of them, that Compare has yet to compare.
type pair struct {
	w, g       *Node
	hasW, hasG bool // false when that tree has no node at this place
	step       int
}

// Compare reports how two trees differ. A converter from another markup uses it
// to check the Markdown that it writes: it builds the tree it expects for a
// post, parses its Markdown with [Parse], and compares the two. An empty result
// means that the trees are equal.
//
// Compare walks both trees depth first, in document order, and compares kind,
// name, attributes and text. An empty attribute map and a nil one are equal.
// When two nodes differ in kind or name, Compare reports that one difference and
// does not look below them. When one tree has fewer children than the other, it
// reports each child that is left over. A nil want or got gives one diff at
// "/", unless both are nil.
//
// Compare walks with an explicit stack, so a deep tree cannot exhaust the call
// stack. It also keeps the pairs it has met and skips one it meets twice, so a
// cycle ends. A node that appears in more than one place is therefore compared
// once. [Validate] rejects both forms.
func Compare(want, got *Node) []Diff {
	if want == nil && got == nil {
		return nil
	}
	if want == nil || got == nil {
		return []Diff{{Path: "/", Want: describe(want), Got: describe(got)}}
	}
	var diffs []Diff
	steps := []step{{parent: -1}}
	stack := []pair{{w: want, g: got, hasW: true, hasG: true}}
	seen := map[[2]*Node]struct{}{}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if !cur.hasW || !cur.hasG {
			d := Diff{Path: pathOf(steps, cur.step)}
			if cur.hasW {
				d.Want = describe(cur.w)
			} else {
				d.Got = describe(cur.g)
			}
			diffs = append(diffs, d)
			continue
		}
		w, g := cur.w, cur.g
		if w == nil || g == nil {
			if w != g {
				diffs = append(diffs, Diff{Path: pathOf(steps, cur.step), Want: describe(w), Got: describe(g)})
			}
			continue
		}
		key := [2]*Node{w, g}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		if w.Kind != g.Kind || (w.Kind == Element && w.Name != g.Name) {
			diffs = append(diffs, Diff{Path: pathOf(steps, cur.step), Want: describe(w), Got: describe(g)})
			continue
		}
		if !sameAttrs(w.Attrs, g.Attrs) {
			diffs = append(diffs, Diff{Path: pathOf(steps, cur.step), Want: attrList(w.Attrs), Got: attrList(g.Attrs)})
		}
		if w.Text != g.Text {
			diffs = append(diffs, Diff{
				Path: pathOf(steps, cur.step),
				Want: strconv.Quote(w.Text),
				Got:  strconv.Quote(g.Text),
			})
		}
		// Push the children last to first, so that the first child is next off
		// the stack and the diffs come in document order.
		n := max(len(w.Children), len(g.Children))
		for i := n - 1; i >= 0; i-- {
			c := pair{hasW: i < len(w.Children), hasG: i < len(g.Children)}
			if c.hasW {
				c.w = w.Children[i]
			}
			if c.hasG {
				c.g = g.Children[i]
			}
			name := stepName(c.w)
			if !c.hasW {
				name = stepName(c.g)
			}
			steps = append(steps, step{parent: cur.step, name: name, index: i})
			c.step = len(steps) - 1
			stack = append(stack, c)
		}
	}
	return diffs
}

// pathOf builds the path of the node whose step is at index i.
func pathOf(steps []step, i int) string {
	if i == 0 {
		return "/"
	}
	var segs []string
	for ; i > 0; i = steps[i].parent {
		segs = append(segs, steps[i].name+"["+strconv.Itoa(steps[i].index)+"]")
	}
	var sb strings.Builder
	for j := len(segs) - 1; j >= 0; j-- {
		sb.WriteByte('/')
		sb.WriteString(segs[j])
	}
	return sb.String()
}

// stepName is the name of a node in a path.
func stepName(n *Node) string {
	switch {
	case n == nil:
		return "nil"
	case n.Kind == Root:
		return "#root"
	case n.Kind == Text:
		return "#text"
	}
	return n.Name
}

// describe names a node in a Want or Got field of a [Diff].
func describe(n *Node) string {
	switch {
	case n == nil:
		return "nil"
	case n.Kind == Root:
		return "root"
	case n.Kind == Text:
		return "text " + strconv.Quote(n.Text)
	}
	return n.Name
}

// sameAttrs reports whether two attribute maps hold the same pairs. A nil map
// and an empty one are the same.
func sameAttrs(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

// attrList writes attributes as "k=v" pairs sorted by name and joined by ",".
func attrList(a map[string]string) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for i, k := range keys {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(k + "=" + a[k])
	}
	return sb.String()
}
