package docengine

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// extractDocx parses a .docx into prose blocks. It streams word/document.xml with
// a token decoder (never xml.Unmarshal of the whole part — a large doc's body is
// tens of MB, PRD §12), tracking paragraph style so Heading N / Title map to
// heading blocks and numbered/bulleted paragraphs to list items. Table cells are
// captured as paragraphs in v1 (structured table serialization is a follow-up).
func extractDocx(ctx context.Context, data []byte) ([]Block, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	f, ok := zipFile(zr, "word/document.xml")
	if !ok {
		return nil, fmt.Errorf("%w: docx missing word/document.xml", ErrInvalid)
	}
	rc, err := openEntry(f)
	if err != nil {
		return nil, fmt.Errorf("%w: open docx body: %v", ErrInvalid, err)
	}
	defer rc.Close()
	return parseWordXML(ctx, rc)
}

// parseWordXML streams the WordprocessingML body. Element local names are matched
// (ignoring the w: namespace): p=paragraph, t=text run, tab/br/cr=whitespace,
// pStyle=paragraph style, numPr=list marker. A non-EOF decoder error (malformed
// XML) is returned as ErrInvalid rather than silently truncating the document.
func parseWordXML(ctx context.Context, r io.Reader) ([]Block, error) {
	dec := xml.NewDecoder(r)
	var (
		blocks   []Block
		para     strings.Builder
		styleVal string
		isList   bool
		inText   bool
	)

	flush := func() {
		text := strings.TrimSpace(para.String())
		para.Reset()
		style, list := styleVal, isList
		styleVal, isList = "", false
		if text == "" {
			return
		}
		switch lvl := headingLevelFromStyle(style); {
		case lvl > 0:
			blocks = append(blocks, Block{Kind: BlockHeading, Level: lvl, Text: text, Anchor: Anchor{Kind: AnchorSection}})
		case list:
			blocks = append(blocks, Block{Kind: BlockListItem, Text: text, Anchor: Anchor{Kind: AnchorSection}})
		default:
			blocks = append(blocks, Block{Kind: BlockParagraph, Text: text, Anchor: Anchor{Kind: AnchorSection}})
		}
	}

	for i := 0; ; i++ {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: malformed docx xml: %v", ErrInvalid, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				para.Reset()
				styleVal, isList = "", false
			case "pStyle":
				styleVal = attrValue(t, "val")
			case "numPr":
				isList = true
			case "t":
				inText = true
			case "tab":
				para.WriteByte(' ')
			case "br", "cr":
				para.WriteByte('\n')
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				flush()
			}
		}
	}
	flush() // in case the stream ended mid-body without a closing </w:p>
	return blocks, nil
}

// attrValue returns the value of the first attribute whose local name matches.
func attrValue(e xml.StartElement, local string) string {
	for _, a := range e.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

// headingLevelFromStyle maps a Word paragraph style id to a heading depth:
// "Heading1".."Heading9" -> 1..6 (clamped), "Title" -> 1, "Subtitle" -> 2, a
// bare "Heading" -> 2. Non-heading styles return 0.
func headingLevelFromStyle(s string) int {
	ls := strings.ToLower(strings.TrimSpace(s))
	switch ls {
	case "":
		return 0
	case "title":
		return 1
	case "subtitle":
		return 2
	}
	if strings.HasPrefix(ls, "heading") {
		rest := strings.TrimSpace(strings.TrimPrefix(ls, "heading"))
		if rest == "" {
			return 2
		}
		if n, err := strconv.Atoi(rest); err == nil && n >= 1 {
			if n > 6 {
				n = 6
			}
			return n
		}
	}
	return 0
}
