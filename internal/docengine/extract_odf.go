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

// ODF (.odt/.ods/.odp) is a zip whose content.xml holds the body in the
// OpenDocument XML. The three sub-formats share the tokenizer but map to the
// three document shapes: odt=prose, ods=sheets, odp=slides. A non-EOF decoder
// error (malformed/truncated content.xml) is returned as ErrInvalid rather than
// silently yielding a partial document.

// maxSheetCols bounds a single row's materialised width (Excel's max is 16384),
// so a hostile number-columns-repeated can't blow up a row.
const maxSheetCols = 8192

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
func extractOdt(ctx context.Context, data []byte) ([]Block, error) {
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
			return nil, fmt.Errorf("%w: malformed odt xml: %v", ErrInvalid, err)
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
// numeric values come from office:value (not the formula). number-columns-repeated
// and number-rows-repeated are honoured so column and physical-row positions stay
// accurate without materialising huge empty runs.
func extractOds(ctx context.Context, data []byte) ([]Block, error) {
	rc, err := odfContentReader(data)
	if err != nil {
		return nil, err
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		blocks    []Block
		sheet     string
		row       []string
		col       int // running 0-based column cursor within the row (honours repeats)
		physRow   int // last physical 1-based row consumed within the sheet
		curRow    int // physical 1-based row of the row currently being built
		cell      strings.Builder
		inCell    bool
		repeat    int
		valueAttr string
		valueType string
	)
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
			return nil, fmt.Errorf("%w: malformed ods xml: %v", ErrInvalid, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "table":
				sheet = attrValue(t, "name")
				physRow = 0
			case "table-row":
				row, col = nil, 0
				rowRepeat := atoiOr(attrValue(t, "number-rows-repeated"), 1)
				curRow = physRow + 1 // first physical row of this (possibly repeated) group
				physRow += rowRepeat
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
				row, col = placeCell(row, col, validUTF8(val), repeat)
			case "table-row":
				cells := trimTrailingEmpty(row)
				if len(cells) > 0 {
					blocks = append(blocks, Block{Kind: BlockSheetRow, Cells: cells, Row: curRow, Anchor: Anchor{Kind: AnchorSheet, Sheet: sheet}})
				}
			}
		}
	}
	return blocks, nil
}

// extractOdp streams an OpenDocument Presentation into slide blocks, one per
// draw:page in document order.
func extractOdp(ctx context.Context, data []byte) ([]Block, error) {
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
			return nil, fmt.Errorf("%w: malformed odp xml: %v", ErrInvalid, err)
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

// placeCell writes a cell value at the running column cursor, honouring
// number-columns-repeated. It materialises leading empties only up to a non-empty
// cell's column (bounded by maxSheetCols), so the true column offset of real data
// is preserved without expanding huge trailing empty runs, then advances the
// cursor by the full repeat. Empty cells contribute no data but still move the
// cursor, keeping later columns correctly positioned.
func placeCell(row []string, col int, val string, repeat int) ([]string, int) {
	if repeat < 1 {
		repeat = 1
	}
	if val != "" {
		for len(row) < col && len(row) < maxSheetCols {
			row = append(row, "")
		}
		for i := 0; i < repeat && len(row) < maxSheetCols; i++ {
			row = append(row, val)
		}
	}
	return row, col + repeat
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
