package docengine

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
)

// extractDelimited streams a CSV/TSV file into SheetRow blocks (one implicit
// sheet, so Anchor.Sheet is empty). Ragged rows and lazy quotes are tolerated —
// real-world exports are messy — but a genuine parse error is returned as
// ErrInvalid so the caller can tell truncation from a complete document (rather
// than silently returning a partial success).
func extractDelimited(ctx context.Context, data []byte, sep rune) ([]Block, error) {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = sep
	r.FieldsPerRecord = -1 // allow ragged rows
	r.LazyQuotes = true
	r.ReuseRecord = false

	var blocks []Block
	row := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: malformed delimited input: %v", ErrInvalid, err)
		}
		row++
		cells := make([]string, len(rec))
		for i, c := range rec {
			cells[i] = validUTF8(c)
		}
		blocks = append(blocks, Block{Kind: BlockSheetRow, Cells: cells, Row: row, Anchor: Anchor{Kind: AnchorSheet}})
	}
	return blocks, nil
}
