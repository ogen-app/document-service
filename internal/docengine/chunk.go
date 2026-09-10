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
	target, overlap, maxChars := resolveSizing(opts)
	switch sh {
	case shapeSheets:
		return chunkSheets(blocks, target, maxChars)
	case shapeSlides:
		return chunkSlides(blocks, target, maxChars)
	default:
		return chunkProse(blocks, target, overlap, maxChars)
	}
}

func resolveSizing(opts Options) (target, overlap, maxChars int) {
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
	overlap = opts.OverlapChars
	if overlap < 0 {
		overlap = 0
	}
	if overlap >= target {
		overlap = target - 1 // overlap must be strictly less than a chunk's target
	}
	return target, overlap, maxChars
}

// chunkProse groups body blocks (paragraphs, list items, captions) under their
// heading breadcrumb. A chunk is contiguous body under one heading path, split at
// paragraph boundaries on overflow; the breadcrumb is prepended to every chunk so
// each is self-describing (PRD §8). When a section overflows, up to `overlap`
// chars of its tail are carried into the next chunk so context isn't lost at the
// split. No emitted Chunk.Text exceeds maxChars (the breadcrumb is included in
// that bound).
func chunkProse(blocks []Block, target, overlap, maxChars int) []Chunk {
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
		// Enforce maxChars on the FINAL text (breadcrumb included), rune-safe.
		for _, piece := range splitToMax(validUTF8(full), maxChars) {
			chunks = append(chunks, Chunk{
				Index:       len(chunks),
				Text:        piece,
				SourceLabel: label,
				Anchor:      Anchor{Kind: baseKind, HeadingPath: append([]string(nil), curPath...)},
				TokenCount:  estimateTokens(piece),
			})
		}
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
			// Overflow within a section: carry the tail into the next chunk.
			tail := overlapTail(strings.Join(body, "\n\n"), overlap)
			flush()
			if tail != "" {
				body = append(body, tail)
				bodyLen += len(tail)
			}
		}
		body = append(body, t)
		bodyLen += len(t)
	}

	for _, b := range blocks {
		switch b.Kind {
		case BlockHeading:
			flush() // a heading is a section boundary (no overlap across sections)
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
// summary chunk per sheet (PRD §8). Cell ranges use each block's PHYSICAL row
// number so they stay correct across blank rows the extractor skipped. No emitted
// chunk exceeds maxChars.
func chunkSheets(blocks []Block, target, maxChars int) []Chunk {
	var chunks []Chunk
	emit := func(text, label string, a Anchor) {
		for _, piece := range splitToMax(validUTF8(text), maxChars) {
			chunks = append(chunks, Chunk{
				Index:       len(chunks),
				Text:        piece,
				SourceLabel: label,
				Anchor:      a,
				TokenCount:  estimateTokens(piece),
			})
		}
	}

	for _, sheet := range groupBySheet(blocks) {
		if len(sheet.rows) == 0 {
			continue
		}
		header := sheet.rows[0].cells
		dataRows := sheet.rows[1:]
		lastCol := colLetter(sheetMaxCols(sheet.rows) - 1)
		firstRow := sheet.rows[0].row
		lastRow := sheet.rows[len(sheet.rows)-1].row

		// Summary chunk: name, columns, row count, a few sample rows.
		emit(sheetSummary(sheet.name, header, dataRows), sheetLabel(sheet.name, "summary"),
			Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: fmt.Sprintf("A%d:%s%d", firstRow, lastCol, lastRow)})

		// Data rows grouped to budget, never split.
		var group []string
		groupLen, startRow, endRow := 0, 0, 0
		flush := func() {
			if len(group) == 0 {
				return
			}
			rng := fmt.Sprintf("A%d:%s%d", startRow, lastCol, endRow)
			emit(strings.Join(group, "\n"), sheetLabel(sheet.name, fmt.Sprintf("rows %d-%d", startRow, endRow)),
				Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: rng})
			group = nil
			groupLen = 0
		}
		for _, r := range dataRows {
			line := serializeRow(sheet.name, header, r.cells)
			if line == "" {
				continue
			}
			if len(line) > maxChars {
				flush()
				emit(line, sheetLabel(sheet.name, fmt.Sprintf("row %d", r.row)),
					Anchor{Kind: AnchorSheet, Sheet: sheet.name, CellRange: fmt.Sprintf("A%d:%s%d", r.row, lastCol, r.row)})
				continue
			}
			if groupLen > 0 && groupLen+len(line) > target {
				flush()
			}
			if len(group) == 0 {
				startRow = r.row
			}
			endRow = r.row
			group = append(group, line)
			groupLen += len(line)
		}
		flush()
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
		anchor := Anchor{Kind: AnchorSlide, Slide: startSlide}
		for _, piece := range splitToMax(validUTF8(strings.Join(buf, "\n\n")), maxChars) {
			chunks = append(chunks, Chunk{
				Index:       len(chunks),
				Text:        piece,
				SourceLabel: label,
				Anchor:      anchor,
				TokenCount:  estimateTokens(piece),
			})
		}
		buf = nil
		bufLen = 0
	}

	for _, sl := range slides {
		text := strings.TrimSpace(validUTF8(sl.text))
		if text == "" {
			continue
		}
		block := fmt.Sprintf("Slide %d\n%s", sl.num, text)
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

type sheetRow struct {
	cells []string
	row   int // physical 1-based source row
}

type sheetGroup struct {
	name string
	rows []sheetRow
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
		groups[i].rows = append(groups[i].rows, sheetRow{cells: b.Cells, row: b.Row})
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

func sheetSummary(sheet string, header []string, dataRows []sheetRow) string {
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
			b.WriteString(serializeRow(sheet, header, r.cells))
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

func sheetMaxCols(rows []sheetRow) int {
	m := 0
	for _, r := range rows {
		if len(r.cells) > m {
			m = len(r.cells)
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

// splitToMax returns text unchanged when it fits maxChars, else rune-safe pieces
// each within maxChars.
func splitToMax(text string, maxChars int) []string {
	if maxChars <= 0 || len(text) <= maxChars {
		return []string{text}
	}
	return hardSplit(text, maxChars)
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

// overlapTail returns up to n trailing chars of s, snapped to a rune boundary and
// (where possible) starting after a whitespace so the carry-over doesn't begin
// mid-word. Empty when n <= 0.
func overlapTail(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return strings.TrimSpace(s)
	}
	start := len(s) - n
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	tail := s[start:]
	if i := strings.IndexAny(tail, " \n\t"); i >= 0 && i < len(tail)-1 {
		tail = tail[i+1:]
	}
	return strings.TrimSpace(tail)
}
