package docengine

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// chunkBlocks turns an extractor's Block stream into embedding-ready chunks,
// dispatching on the document shape (PRD §8). Chunk sizing comes from opts, with
// engine defaults when zero.
func chunkBlocks(sh shape, blocks []Block, opts Options) []Chunk {
	target, maxChars := resolveSizing(opts)
	switch sh {
	case shapeSheets:
		return chunkSheets(blocks, target, maxChars)
	case shapeSlides:
		return chunkSlides(blocks, target, maxChars)
	default:
		return chunkProse(blocks, target, maxChars)
	}
}

func resolveSizing(opts Options) (target, maxChars int) {
	target = opts.TargetChars
	if target <= 0 {
		target = defaultTargetChars
	}
	maxChars = opts.MaxChars
	if maxChars <= 0 {
		maxChars = defaultMaxChars
	}
	if maxChars < target {
		maxChars = target
	}
	return target, maxChars
}

// chunkProse groups body blocks (paragraphs, list items, captions) under their
// heading breadcrumb. A chunk is contiguous body under one heading path, split at
// paragraph boundaries on overflow; the breadcrumb is prepended to every chunk so
// each is self-describing (PRD §8). Headings maintain a level-indexed stack.
func chunkProse(blocks []Block, target, maxChars int) []Chunk {
	// A prose stream carries one anchor kind (section for documents, email for
	// .eml); take it from the body blocks so the kind survives chunking.
	baseKind := AnchorSection
	for _, b := range blocks {
		if b.Kind != BlockHeading && b.Anchor.Kind == AnchorEmail {
			baseKind = AnchorEmail
			break
		}
	}

	var (
		chunks  []Chunk
		stack   []string // heading path by level (may hold "" for skipped levels)
		curPath []string // heading path snapshot for the accumulating body
		body    []string
		bodyLen int
	)

	flush := func() {
		if len(body) == 0 {
			return
		}
		label := strings.Join(curPath, " › ")
		text := strings.Join(body, "\n\n")
		full := text
		if label != "" {
			full = label + "\n\n" + text
		}
		full = validUTF8(full)
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Text:        full,
			SourceLabel: label,
			Anchor:      Anchor{Kind: baseKind, HeadingPath: append([]string(nil), curPath...)},
			TokenCount:  estimateTokens(full),
		})
		body = nil
		bodyLen = 0
	}

	addBody := func(t string) {
		t = strings.TrimSpace(validUTF8(t))
		if t == "" {
			return
		}
		if len(curPath) == 0 {
			curPath = nonEmpty(stack)
		}
		if len(t) > maxChars {
			flush()
			for _, part := range hardSplit(t, maxChars) {
				body = []string{part}
				bodyLen = len(part)
				flush()
			}
			return
		}
		if bodyLen > 0 && bodyLen+len(t) > target {
			flush()
		}
		body = append(body, t)
		bodyLen += len(t)
	}

	for _, b := range blocks {
		switch b.Kind {
		case BlockHeading:
			flush() // a heading is a section boundary
			lvl := b.Level
			if lvl < 1 {
				lvl = 1
			}
			if lvl-1 < len(stack) {
				stack = stack[:lvl-1]
			} else {
				for len(stack) < lvl-1 {
					stack = append(stack, "")
				}
			}
			stack = append(stack, strings.TrimSpace(validUTF8(b.Text)))
			curPath = nonEmpty(stack)
		default:
			addBody(b.Text)
		}
	}
	flush()
	return chunks
}

// chunkSheets serializes tabular data as labelled fields (never a raw grid),
// groups rows to fill the budget without ever splitting a row, and emits one
// summary chunk per sheet so "what's in this spreadsheet" is answerable (PRD §8).
// Rows arrive in order; the first row of each sheet is treated as the header.
func chunkSheets(blocks []Block, target, maxChars int) []Chunk {
	var chunks []Chunk
	emit := func(text, label string, a Anchor) {
		text = validUTF8(text)
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Text:        text,
			SourceLabel: label,
			Anchor:      a,
			TokenCount:  estimateTokens(text),
		})
	}

	for _, sheet := range groupBySheet(blocks) {
		if len(sheet.rows) == 0 {
			continue
		}
		header := sheet.rows[0]
		dataRows := sheet.rows[1:]
		lastCol := colLetter(maxLen(sheet.rows) - 1)

		// Summary chunk: name, columns, row count, a few sample rows.
		emit(sheetSummary(sheet.name, header, dataRows), sheetLabel(sheet.name, "summary"),
			Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: fmt.Sprintf("A1:%s%d", lastCol, len(sheet.rows))})

		// Data rows grouped to budget, never split.
		var group []string
		groupLen, startRow := 0, 0
		flush := func(endRow int) {
			if len(group) == 0 {
				return
			}
			rng := fmt.Sprintf("A%d:%s%d", startRow, lastCol, endRow)
			label := sheetLabel(sheet.name, fmt.Sprintf("rows %d-%d", startRow, endRow))
			emit(strings.Join(group, "\n"), label,
				Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: rng})
			group = nil
			groupLen = 0
		}
		for i, row := range dataRows {
			rowNum := i + 2 // header is row 1
			line := serializeRow(sheet.name, header, row)
			if line == "" {
				continue
			}
			if len(line) > maxChars {
				flush(rowNum - 1)
				for _, part := range hardSplit(line, maxChars) {
					emit(part, sheetLabel(sheet.name, fmt.Sprintf("row %d", rowNum)),
						Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: fmt.Sprintf("A%d:%s%d", rowNum, lastCol, rowNum)})
				}
				continue
			}
			if groupLen > 0 && groupLen+len(line) > target {
				flush(rowNum - 1)
			}
			if len(group) == 0 {
				startRow = rowNum
			}
			group = append(group, line)
			groupLen += len(line)
		}
		flush(len(sheet.rows))
	}
	return chunks
}

// chunkSlides keeps each slide atomic (title + body + speaker notes), merging a
// few consecutive slides only when they fit the budget (PRD §8).
func chunkSlides(blocks []Block, target, maxChars int) []Chunk {
	var chunks []Chunk
	slides := groupBySlide(blocks)

	var buf []string
	bufLen, startSlide, endSlide := 0, 0, 0
	flush := func() {
		if len(buf) == 0 {
			return
		}
		label := fmt.Sprintf("Slide %d", startSlide)
		if endSlide != startSlide {
			label = fmt.Sprintf("Slides %d-%d", startSlide, endSlide)
		}
		text := validUTF8(strings.Join(buf, "\n\n"))
		chunks = append(chunks, Chunk{
			Index:       len(chunks),
			Text:        text,
			SourceLabel: label,
			Anchor:      Anchor{Kind: AnchorSlide, Slide: startSlide},
			TokenCount:  estimateTokens(text),
		})
		buf = nil
		bufLen = 0
	}

	for _, sl := range slides {
		text := strings.TrimSpace(validUTF8(sl.text))
		if text == "" {
			continue
		}
		block := fmt.Sprintf("Slide %d\n%s", sl.num, text)
		if len(block) > maxChars {
			flush()
			for _, part := range hardSplit(block, maxChars) {
				startSlide, endSlide = sl.num, sl.num
				buf = []string{part}
				flush()
			}
			continue
		}
		if bufLen > 0 && bufLen+len(block) > target {
			flush()
		}
		if len(buf) == 0 {
			startSlide = sl.num
		}
		endSlide = sl.num
		buf = append(buf, block)
		bufLen += len(block)
	}
	flush()
	return chunks
}

// ---- sheet helpers ----

type sheetGroup struct {
	name string
	rows [][]string
}

func groupBySheet(blocks []Block) []sheetGroup {
	var groups []sheetGroup
	idx := map[string]int{}
	for _, b := range blocks {
		if b.Kind != BlockSheetRow {
			continue
		}
		name := b.Anchor.Sheet
		i, ok := idx[name]
		if !ok {
			i = len(groups)
			idx[name] = i
			groups = append(groups, sheetGroup{name: name})
		}
		groups[i].rows = append(groups[i].rows, b.Cells)
	}
	return groups
}

// serializeRow renders one data row as labelled fields, repeating the sheet name
// and column labels so the chunk is self-describing. Empty cells are omitted.
func serializeRow(sheet string, header, row []string) string {
	var parts []string
	if sheet != "" {
		parts = append(parts, fmt.Sprintf("Sheet %q", sheet))
	}
	for i, cell := range row {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			continue
		}
		col := fmt.Sprintf("Col %s", colLetter(i))
		if i < len(header) {
			if h := strings.TrimSpace(header[i]); h != "" {
				col = h
			}
		}
		parts = append(parts, fmt.Sprintf("%s: %s", col, cell))
	}
	return strings.Join(parts, " | ")
}

func sheetSummary(sheet string, header []string, dataRows [][]string) string {
	var b strings.Builder
	if sheet != "" {
		fmt.Fprintf(&b, "Sheet %q — ", sheet)
	}
	cols := make([]string, 0, len(header))
	for _, h := range header {
		if h = strings.TrimSpace(h); h != "" {
			cols = append(cols, h)
		}
	}
	fmt.Fprintf(&b, "columns: %s. %d data rows.", strings.Join(cols, ", "), len(dataRows))
	sample := dataRows
	if len(sample) > 3 {
		sample = sample[:3]
	}
	if len(sample) > 0 {
		b.WriteString("\nSample rows:")
		for _, r := range sample {
			b.WriteString("\n")
			b.WriteString(serializeRow(sheet, header, r))
		}
	}
	return b.String()
}

func sheetLabel(sheet, suffix string) string {
	if sheet != "" {
		return fmt.Sprintf("Sheet %q %s", sheet, suffix)
	}
	// Capitalize the first letter for a standalone label ("Rows 2-40").
	if suffix == "" {
		return ""
	}
	return strings.ToUpper(suffix[:1]) + suffix[1:]
}

// colLetter converts a 0-based column index to a spreadsheet column label
// (0->A, 25->Z, 26->AA). Negative indexes clamp to "A".
func colLetter(n int) string {
	if n < 0 {
		return "A"
	}
	var s string
	for {
		s = string(rune('A'+n%26)) + s
		n = n/26 - 1
		if n < 0 {
			break
		}
	}
	return s
}

func maxLen(rows [][]string) int {
	m := 0
	for _, r := range rows {
		if len(r) > m {
			m = len(r)
		}
	}
	return m
}

// ---- slide helpers ----

type slideGroup struct {
	num  int
	text string
}

func groupBySlide(blocks []Block) []slideGroup {
	var groups []slideGroup
	idx := map[int]int{}
	for _, b := range blocks {
		n := b.Anchor.Slide
		i, ok := idx[n]
		if !ok {
			i = len(groups)
			idx[n] = i
			groups = append(groups, slideGroup{num: n})
		}
		if t := strings.TrimSpace(b.Text); t != "" {
			if groups[i].text != "" {
				groups[i].text += "\n"
			}
			groups[i].text += t
		}
	}
	return groups
}

// ---- shared text helpers ----

// nonEmpty returns the non-empty entries of s in order (drops padding for skipped
// heading levels), as a fresh slice.
func nonEmpty(s []string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}

// validUTF8 guarantees a proto3-safe string: invalid bytes become the
// replacement rune (CON-110 — proto3 string marshaling rejects invalid UTF-8).
func validUTF8(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "�")
}

// hardSplit chops s into pieces of at most maxChars bytes, never splitting a
// rune. Used for a pathological single paragraph/row larger than the budget.
func hardSplit(s string, maxChars int) []string {
	var parts []string
	for len(s) > maxChars {
		cut := maxChars
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			cut = maxChars
		}
		parts = append(parts, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		parts = append(parts, s)
	}
	return parts
}
