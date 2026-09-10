package docengine

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestExtractDocx_MalformedXMLIsInvalid guards that a malformed body surfaces as
// ErrInvalid (terminal InvalidArgument) rather than a silent partial success.
func TestExtractDocx_MalformedXMLIsInvalid(t *testing.T) {
	// An undefined entity is a hard XML syntax error (non-EOF).
	data := makeDocx(t, `<w:document><w:body><w:p><w:r><w:t>hi &nope; there</w:t></w:r></w:p></w:body></w:document>`)
	if _, err := New().Extract(data, Options{Filename: "x.docx"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("malformed docx err = %v, want ErrInvalid", err)
	}
}

// TestExtractRtf_SkipsUnicodeFallback verifies the ANSI fallback char after a \u
// is dropped (no stray "?") while the decoded rune is kept. The RTF is built by
// concatenation so no literal backslash-u appears in the source.
func TestExtractRtf_SkipsUnicodeFallback(t *testing.T) {
	bs := "\\"
	rtf := "{" + bs + "rtf1 A" + bs + "u8226?B}" // 舦 = U+2022 bullet, '?' is the fallback
	res, err := New().Extract([]byte(rtf), Options{Filename: "x.rtf"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var joined string
	for _, c := range res.Chunks {
		joined += c.Text
	}
	if strings.Contains(joined, "?") {
		t.Fatalf("unicode fallback char not skipped: %q", joined)
	}
	if !strings.Contains(joined, "•") || !strings.Contains(joined, "A") || !strings.Contains(joined, "B") {
		t.Fatalf("expected A•B, got %q", joined)
	}
}

// TestExtractOds_PreservesColumnOffset verifies a run of empty repeated cells
// keeps a later non-empty cell at its true column index (no left shift).
func TestExtractOds_PreservesColumnOffset(t *testing.T) {
	content := `<office:document-content xmlns:office="o" xmlns:table="t" xmlns:text="x">
<office:body><office:spreadsheet><table:table table:name="S">
<table:table-row>
<table:table-cell office:value-type="string"><text:p>A</text:p></table:table-cell>
<table:table-cell table:number-columns-repeated="3"/>
<table:table-cell office:value-type="string"><text:p>E</text:p></table:table-cell>
</table:table-row>
</table:table></office:spreadsheet></office:body></office:document-content>`
	blocks, err := extractOds(context.Background(), odfZip(t, "application/vnd.oasis.opendocument.spreadsheet", content))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("want 1 row block, got %d", len(blocks))
	}
	cells := blocks[0].Cells
	if len(cells) != 5 || cells[0] != "A" || cells[4] != "E" {
		t.Fatalf("column offset not preserved: %#v", cells)
	}
}

// TestExtractEml_SkipsAttachmentBody ensures a text/plain attachment placed
// before the real body is not mistaken for the body.
func TestExtractEml_SkipsAttachmentBody(t *testing.T) {
	raw := "From: a@b.com\nContent-Type: multipart/mixed; boundary=B\n\n" +
		"--B\nContent-Type: text/plain\nContent-Disposition: attachment; filename=a.txt\n\n" +
		"ATTACHMENT TEXT\n" +
		"--B\nContent-Type: text/plain\n\n" +
		"REAL BODY\n" +
		"--B--\n"
	res, err := New().Extract([]byte(raw), Options{Filename: "m.eml"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	var joined string
	for _, c := range res.Chunks {
		joined += c.Text
	}
	if !strings.Contains(joined, "REAL BODY") || strings.Contains(joined, "ATTACHMENT TEXT") {
		t.Fatalf("attachment not skipped / body missing: %q", joined)
	}
}
