// Package postbody derives the stored columns of a post body from its tree. The
// importer and the board's write path both call it, so a post that gobb saves
// and a post that gobb imports get the same JSON and the same search text from
// the same tree.
package postbody

import (
	"encoding/json"
	"fmt"

	"github.com/evanwtf/gobb/markup"
)

// Columns returns the JSON of the tree, for body_tree, and its plain text, for
// body_text. The caller has validated the tree.
func Columns(tree *markup.Node) (data []byte, text string, err error) {
	data, err = json.Marshal(tree)
	if err != nil {
		return nil, "", fmt.Errorf("postbody: encode tree: %w", err)
	}
	return data, markup.Render(tree, markup.RenderContext{}).Text, nil
}
