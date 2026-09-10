package docengine

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ODF (.odt/.ods/.odp) is a zip whose content.xml holds the body in the
// OpenDocument XML. The three sub-formats share the tokenizer but map to the
// three document shapes: odt=prose, ods=sheets, odp=slides.

// odfContentReader opens content.xml from an ODF archive.
func odfContentReader(data []byte) (io.ReadCloser, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	f, ok := zipFile(zr, "content.xml")
	if !ok {
		return nil, fmt.Errorf("%w: odf missing content.xml", ErrInvalid)
	}
	rc, err := openEntry(f)
	if err != nil {
		return nil, fmt.Errorf("%w: open content.xml: %v", ErrInvalid, err)
	}
	return rc, nil
}

// extractOdt streams an OpenDocument Text body into prose blocks: text:h ->
// heading (text:outline-level = depth), text:p -> paragraph.
func extractOdt(data []byte) ([]Block, error) {
	rc, err := odfContentReader(data)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		blocks    []Block
		buf       strings.Builder
		capturing bool
		heading   bool
		level     int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "h":
				capturing, heading = true, true
				level = atoiOr(attrValue(t, "outline-level"), 1)
				buf.Reset()
			case "p":
				if !capturing {
					capturing, heading = true, false
					buf.Reset()
				}
			case "tab", "s":
				if capturing {
					buf.WriteByte(' ')
				}
			case "line-break":
				if capturing {
					buf.WriteByte('\n')
				}
			}
		case xml.CharData:
			if capturing {
				buf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "h":
				if capturing && heading {
					blocks = appendProse(blocks, BlockHeading, clampLevel(level), buf.String())
					capturing = false
				}
			case "p":
				if capturing && !heading {
					blocks = appendProse(blocks, BlockParagraph, 0, buf.String())
					capturing = false
				}
			}
		}
	}
	return blocks, nil
}

// extractOds streams an OpenDocument Spreadsheet into SheetRow blocks. Computed
// numeric values come from office:value (not the formula); repeated empty cells
// (number-columns-repeated) are not expanded.
func extractOds(data []byte) ([]Block, error) {
	rc, err := odfContentReader(data)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		blocks    []Block
		sheet     string
		cells     []string
		cell      strings.Builder
		inCell    bool
		repeat    int
		valueAttr string
		valueType string
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "table":
				sheet = attrValue(t, "name")
			case "table-row":
				cells = nil
			case "table-cell", "covered-table-cell":
				inCell = true
				cell.Reset()
				repeat = atoiOr(attrValue(t, "number-columns-repeated"), 1)
				valueAttr = attrValue(t, "value")
				valueType = attrValue(t, "value-type")
			}
		case xml.CharData:
			if inCell {
				cell.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "table-cell", "covered-table-cell":
				inCell = false
				val := strings.TrimSpace(cell.String())
				if valueAttr != "" && isNumericValueType(valueType) {
					val = valueAttr
				}
				cells = appendRepeated(cells, validUTF8(val), repeat)
			case "table-row":
				row := trimTrailingEmpty(cells)
				if len(row) > 0 {
					blocks = append(blocks, Block{Kind: BlockSheetRow, Cells: row, Anchor: Anchor{Kind: AnchorSheet, Sheet: sheet}})
				}
			}
		}
	}
	return blocks, nil
}

// extractOdp streams an OpenDocument Presentation into slide blocks, one per
// draw:page in document order.
func extractOdp(data []byte) ([]Block, error) {
	rc, err := odfContentReader(data)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		blocks   []Block
		buf      strings.Builder
		inPage   bool
		slideNum int
	)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "page" {
				inPage = true
				slideNum++
				buf.Reset()
			}
		case xml.CharData:
			if inPage {
				buf.Write(t)
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "h":
				if inPage {
					buf.WriteByte('\n')
				}
			case "page":
				if inPage {
					if text := strings.TrimSpace(buf.String()); text != "" {
						blocks = append(blocks, Block{Kind: BlockSlideBody, Text: text, Anchor: Anchor{Kind: AnchorSlide, Slide: slideNum}})
					}
					inPage = false
				}
			}
		}
	}
	return blocks, nil
}

// ---- small ODF helpers ----

func appendProse(blocks []Block, kind BlockKind, level int, text string) []Block {
	if strings.TrimSpace(text) == "" {
		return blocks
	}
	return append(blocks, Block{Kind: kind, Level: level, Text: text, Anchor: Anchor{Kind: AnchorSection}})
}

// appendRepeated appends val `repeat` times, but never expands repeated EMPTY
// cells (ODF pads trailing/blank cells with huge number-columns-repeated counts),
// and caps a non-empty repeat so a hostile file can't blow up the row.
func appendRepeated(cells []string, val string, repeat int) []string {
	if repeat < 1 {
		repeat = 1
	}
	if val == "" {
		if repeat <= 64 {
			for range repeat {
				cells = append(cells, "")
			}
		}
		return cells
	}
	if repeat > 1024 {
		repeat = 1024
	}
	for range repeat {
		cells = append(cells, val)
	}
	return cells
}

func isNumericValueType(t string) bool {
	switch t {
	case "float", "currency", "percentage":
		return true
	default:
		return false
	}
}

func trimTrailingEmpty(cells []string) []string {
	end := len(cells)
	for end > 0 && strings.TrimSpace(cells[end-1]) == "" {
		end--
	}
	return append([]string(nil), cells[:end]...)
}

func atoiOr(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return n
	}
	return def
}

func clampLevel(n int) int {
	if n < 1 {
		return 1
	}
	if n > 6 {
		return 6
	}
	return n
}
