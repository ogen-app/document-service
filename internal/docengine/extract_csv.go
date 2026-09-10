package docengine

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
)

// extractDelimited streams a CSV/TSV file into SheetRow blocks (one implicit
// sheet, so Anchor.Sheet is empty). Ragged rows and lazy quotes are tolerated —
// real-world exports are messy; a mid-file parse error stops extraction
// best-effort with whatever parsed so far.
func extractDelimited(data []byte, sep rune) []Block {
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = sep
	r.FieldsPerRecord = -1 // allow ragged rows
	r.LazyQuotes = true
	r.ReuseRecord = false

	var blocks []Block
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Best-effort: stop at the first unrecoverable parse error, keep what we
			// have. An all-bad file yields zero blocks -> asset ready with 0 chunks.
			break
		}
		cells := make([]string, len(rec))
		for i, c := range rec {
			cells[i] = validUTF8(c)
		}
		blocks = append(blocks, Block{Kind: BlockSheetRow, Cells: cells, Anchor: Anchor{Kind: AnchorSheet}})
	}
	return blocks
}
