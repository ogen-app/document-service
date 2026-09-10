package docengine

// A Block is the normalized unit every extractor emits, so one chunker serves
// every format (PRD §7). Extractors MUST NOT pre-flatten structure into a string
// — they emit typed blocks, and the chunker decides how to group and serialize
// them per document shape.
type Block struct {
	Kind  BlockKind
	Level int      // heading depth (1..N) or list depth; 0 when n/a
	Text  string   // block text (already valid UTF-8); empty for pure Cells rows
	Cells []string // TableRow / SheetRow only — never pre-joined into Text
	// Anchor locates the block in its source. The chunker copies/refines it onto
	// each emitted chunk (e.g. widening a page range, naming a cell range).
	Anchor Anchor
}

// BlockKind tags a block so the chunker can specialize (prose vs table vs slide
// vs sheet). Mirrors PRD §7.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockHeading
	BlockListItem
	BlockTableRow
	BlockSlideTitle
	BlockSlideBody
	BlockNote // speaker notes
	BlockCaption
	BlockSheetRow
	BlockSheetSummary
)

// shape is the document's overall structure, chosen by the extractor; it selects
// which chunking strategy runs over the block stream.
type shape int

const (
	shapeProse  shape = iota // docx/odt/epub/html/rtf/txt/eml flow
	shapeSheets              // xlsx/ods/csv tabular
	shapeSlides              // pptx/odp decks
)
