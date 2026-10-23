// Package markup defines the document tree that gobb stores for every post,
// signature and private message, and the schema that tree must follow.
//
// Members write Markdown. The parser turns the source into a tree, and the
// renderer turns the tree into HTML. A tree has one [Root], whose descendants
// are [Text] nodes and [Element] nodes. An element has a name and string
// attributes. The tree is stored as JSON.
//
// The schema is the contract for converters (design spec sections 12.1 and
// 12.2). It fixes the element names, the attributes each element may carry and
// the pattern of each value, the children each element may hold, and the
// nesting caps [MaxDepth] and [MaxQuoteDepth]. The root holds blocks: p, h, hr,
// quote, list, pre, and the embeds. Inline elements are text, b, i, code, br,
// url, email, img, spoiler and smiley. A converter that writes trees must
// produce only what [Validate] accepts. [Validate] checks a whole tree against
// the schema. It is the safety gate for every stored tree, so it treats its
// input as hostile: it never panics and it does not recurse.
//
// [CheckLinkURL] and [CheckImageURL] are the one rule for URLs. [Validate], the
// parser and the renderer all call them. [EmbedID] and [IsEmbed] are the one
// rule for embedded media: the parser uses [EmbedID] to turn a pasted URL into
// an ID, and [Validate] requires that a stored ID is already in that form.
//
// Renderer contract: the renderer HTML-escapes every attribute value on output.
// It percent-encodes the address in a mailto: link. It never builds a URL from
// an attribute that [Validate] did not check.
package markup
