package docengine

import (
	"strings"
	"testing"
)

func TestChunkProse_BreadcrumbAndSectionBoundary(t *testing.T) {
	blocks := []Block{
		{Kind: BlockHeading, Level: 1, Text: "Guide"},
		{Kind: BlockHeading, Level: 2, Text: "Auth"},
		{Kind: BlockParagraph, Text: "Use SSO."},
		{Kind: BlockHeading, Level: 2, Text: "Billing"},
		{Kind: BlockParagraph, Text: "Invoices monthly."},
	}
	chunks := chunkProse(blocks, 5000, 0, 6000)
	if len(chunks) != 2 {
		t.Fatalf("want 2 section chunks, got %d", len(chunks))
	}
	if chunks[0].SourceLabel != "Guide › Auth" || !strings.HasPrefix(chunks[0].Text, "Guide › Auth\n\n") {
		t.Fatalf("chunk0 wrong: label=%q text=%q", chunks[0].SourceLabel, chunks[0].Text)
	}
	if chunks[1].SourceLabel != "Guide › Billing" {
		t.Fatalf("chunk1 label wrong: %q", chunks[1].SourceLabel)
	}
	if strings.Join(chunks[1].Anchor.HeadingPath, "/") != "Guide/Billing" {
		t.Fatalf("chunk1 heading path wrong: %+v", chunks[1].Anchor.HeadingPath)
	}
}

func TestChunkProse_SplitsLongSectionAtParagraphBoundary(t *testing.T) {
	p := strings.Repeat("x", 400)
	blocks := []Block{
		{Kind: BlockHeading, Level: 1, Text: "H"},
		{Kind: BlockParagraph, Text: p},
		{Kind: BlockParagraph, Text: p},
	}
	chunks := chunkProse(blocks, 500, 0, 1000)
	if len(chunks) != 2 {
		t.Fatalf("want 2 chunks when the section exceeds target, got %d", len(chunks))
	}
	// Both carry the same breadcrumb — chunks stay self-describing across a split.
	if chunks[0].SourceLabel != "H" || chunks[1].SourceLabel != "H" {
		t.Fatalf("both split chunks should keep the breadcrumb: %q / %q", chunks[0].SourceLabel, chunks[1].SourceLabel)
	}
}

func TestChunkProse_OverlapCarriesTail(t *testing.T) {
	blocks := []Block{
		{Kind: BlockHeading, Level: 1, Text: "H"},
		{Kind: BlockParagraph, Text: "alpha bravo charlie"},
		{Kind: BlockParagraph, Text: "delta echo foxtrot"},
	}
	// Zero overlap: the two paragraphs land in separate chunks with no carry-over.
	zero := chunkProse(blocks, 25, 0, 1000)
	if len(zero) != 2 || strings.Contains(zero[1].Text, "charlie") {
		t.Fatalf("zero-overlap chunk2 should not repeat the tail: %q", zero[1].Text)
	}
	// Nonzero overlap: the tail of chunk 1 is carried into chunk 2.
	over := chunkProse(blocks, 25, 12, 1000)
	if len(over) != 2 {
		t.Fatalf("want 2 chunks, got %d", len(over))
	}
	if !strings.Contains(over[1].Text, "charlie") {
		t.Fatalf("nonzero-overlap chunk2 should carry the previous tail: %q", over[1].Text)
	}
}

func TestChunkSheets_NeverSplitsRowAndSummaryFirst(t *testing.T) {
	blocks := []Block{
		{Kind: BlockSheetRow, Cells: []string{"Region", "ACV"}, Row: 1, Anchor: Anchor{Kind: AnchorSheet, Sheet: "Q3"}},
		{Kind: BlockSheetRow, Cells: []string{"EMEA", "41200"}, Row: 2, Anchor: Anchor{Kind: AnchorSheet, Sheet: "Q3"}},
		{Kind: BlockSheetRow, Cells: []string{"APAC", "80000"}, Row: 5, Anchor: Anchor{Kind: AnchorSheet, Sheet: "Q3"}},
	}
	// Tiny target forces each row into its own chunk, but a row is never split.
	chunks := chunkSheets(blocks, 10, 10000)
	if len(chunks) < 3 {
		t.Fatalf("want summary + 2 row chunks, got %d", len(chunks))
	}
	if !strings.Contains(chunks[0].Text, "Sheet \"Q3\"") || !strings.Contains(chunks[0].SourceLabel, "summary") {
		t.Fatalf("first chunk should be the sheet summary: %q / %q", chunks[0].Text, chunks[0].SourceLabel)
	}
	row := chunks[1]
	if !strings.Contains(row.Text, "Region: EMEA") || !strings.Contains(row.Text, "ACV: 41200") {
		t.Fatalf("row serialization wrong: %q", row.Text)
	}
	if row.Anchor.Sheet != "Q3" || row.Anchor.CellRange != "A2:B2" {
		t.Fatalf("row anchor wrong: %+v", row.Anchor)
	}
}

func TestChunkSlides_AtomicMergeAndLabels(t *testing.T) {
	blocks := []Block{
		{Kind: BlockSlideTitle, Text: "Intro", Anchor: Anchor{Kind: AnchorSlide, Slide: 1}},
		{Kind: BlockSlideBody, Text: "Welcome", Anchor: Anchor{Kind: AnchorSlide, Slide: 1}},
		{Kind: BlockSlideTitle, Text: "Details", Anchor: Anchor{Kind: AnchorSlide, Slide: 2}},
	}
	// Large target: both slides merge into one chunk labelled as a range.
	merged := chunkSlides(blocks, 100000, 100000)
	if len(merged) != 1 || merged[0].SourceLabel != "Slides 1-2" {
		t.Fatalf("merge wrong: %d chunks, label %q", len(merged), func() string {
			if len(merged) > 0 {
				return merged[0].SourceLabel
			}
			return ""
		}())
	}
	if merged[0].Anchor.Kind != AnchorSlide || merged[0].Anchor.Slide != 1 {
		t.Fatalf("slide anchor wrong: %+v", merged[0].Anchor)
	}
	// Tiny target: each slide is its own chunk, never split across slides.
	split := chunkSlides(blocks, 1, 100000)
	if len(split) != 2 || split[0].SourceLabel != "Slide 1" || split[1].SourceLabel != "Slide 2" {
		t.Fatalf("split wrong: %d chunks", len(split))
	}
}

func TestColLetter(t *testing.T) {
	cases := map[int]string{0: "A", 25: "Z", 26: "AA", 27: "AB", 51: "AZ", 52: "BA"}
	for in, want := range cases {
		if got := colLetter(in); got != want {
			t.Fatalf("colLetter(%d) = %q, want %q", in, got, want)
		}
	}
}
