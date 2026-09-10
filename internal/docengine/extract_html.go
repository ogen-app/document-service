package docengine

import (
	"bytes"
	"fmt"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// blockTags maps HTML block elements to the block kind they emit. Headings are
// handled separately (they carry a level).
var blockTags = map[atom.Atom]BlockKind{
	atom.P:          BlockParagraph,
	atom.Li:         BlockListItem,
	atom.Blockquote: BlockParagraph,
	atom.Pre:        BlockParagraph,
	atom.Td:         BlockParagraph,
	atom.Th:         BlockParagraph,
	atom.Caption:    BlockCaption,
	atom.Figcaption: BlockCaption,
}

var headingLevels = map[atom.Atom]int{
	atom.H1: 1, atom.H2: 2, atom.H3: 3, atom.H4: 4, atom.H5: 5, atom.H6: 6,
}

// extractHTML parses HTML/XHTML into prose blocks. It walks the parse tree,
// emitting a block per known block element (its full descendant text), so the
// heading structure feeds the breadcrumb chunker. script/style/head subtrees are
// skipped. Also serves the EPUB spine (future) since chapters are XHTML.
func extractHTML(data []byte) ([]Block, error) {
	doc, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: parse html: %v", ErrInvalid, err)
	}
	var blocks []Block
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Script, atom.Style, atom.Head, atom.Noscript, atom.Template:
				return // skip these subtrees entirely
			}
			if lvl, ok := headingLevels[n.DataAtom]; ok {
				if text := normalizeSpace(textContent(n)); text != "" {
					blocks = append(blocks, Block{Kind: BlockHeading, Level: lvl, Text: text, Anchor: Anchor{Kind: AnchorSection}})
				}
				return // don't re-descend; text already gathered
			}
			if kind, ok := blockTags[n.DataAtom]; ok {
				if text := normalizeSpace(textContent(n)); text != "" {
					blocks = append(blocks, Block{Kind: kind, Text: text, Anchor: Anchor{Kind: AnchorSection}})
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return blocks, nil
}

// textContent concatenates all descendant text of n.
func textContent(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return b.String()
}

// normalizeSpace collapses runs of whitespace to single spaces and trims.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
