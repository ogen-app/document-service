package docengine

import (
	"bytes"
	"fmt"

	"github.com/xuri/excelize/v2"
)

// extractXlsx parses a .xlsx into SheetRow blocks using excelize's streaming row
// iterator (PRD: take excelize, use Rows() not GetRows()). It reads COMPUTED cell
// values (not formula strings), skips hidden and fully-empty sheets, and drops
// blank rows. The sheets chunker then serializes rows as labelled fields and
// emits a per-sheet summary.
func extractXlsx(data []byte) ([]Block, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("%w: open xlsx: %v", ErrInvalid, err)
	}
	defer f.Close()

	var blocks []Block
	for _, name := range f.GetSheetList() {
		if vis, err := f.GetSheetVisible(name); err == nil && !vis {
			continue // skip hidden sheets
		}
		rows, err := f.Rows(name)
		if err != nil {
			continue // unreadable sheet: best-effort, skip it
		}
		for rows.Next() {
			cols, err := rows.Columns() // computed values, as strings
			if err != nil {
				break
			}
			if allBlank(cols) {
				continue
			}
			cells := make([]string, len(cols))
			for i, c := range cols {
				cells[i] = validUTF8(c)
			}
			blocks = append(blocks, Block{Kind: BlockSheetRow, Cells: cells, Anchor: Anchor{Kind: AnchorSheet, Sheet: name}})
		}
		_ = rows.Close()
	}
	return blocks, nil
}

func allBlank(cols []string) bool {
	for _, c := range cols {
		if c != "" {
			return false
		}
	}
	return true
}
