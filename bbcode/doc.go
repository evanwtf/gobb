// Package bbcode defines the document tree that gobb stores for every post,
// signature and private message, and the schema that tree must follow.
//
// A tree has one [Root], whose descendants are [Text] nodes and [Element]
// nodes. An element has a name and string attributes. The tree is stored as
// JSON and the renderer builds HTML from it.
//
// The schema is the contract for bundle trees (design spec section 12.1): it
// fixes the element names, the attributes each element may carry and the
// pattern of each value, the children each element may hold, and the nesting
// caps [MaxDepth] and [MaxQuoteDepth]. [Validate] checks a whole tree against
// the schema. It is the safety gate for imported trees, so it treats its input
// as hostile: it never panics and it does not recurse.
//
// [EmbedID] holds the one rule for the IDs of embedded media. The parser uses
// it to turn a pasted URL into an ID, and [Validate] uses it to require that a
// stored ID is already in that form.
package bbcode
