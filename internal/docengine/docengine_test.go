package docengine

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"
)

func TestExtract_TextSuccess(t *testing.T) {
	res, err := New().Extract([]byte("First paragraph.\n\nSecond paragraph."), Options{Filename: "notes.txt"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "txt" {
		t.Fatalf("format = %q, want txt", res.Format)
	}
	if len(res.Chunks) == 0 || res.Chunks[0].Anchor.Kind != AnchorSection {
		t.Fatalf("unexpected chunks: %+v", res.Chunks)
	}
	if res.Chunks[0].TokenCount == 0 {
		t.Fatal("token count should be estimated")
	}
}

func TestExtract_EmptyIsInvalid(t *testing.T) {
	if _, err := New().Extract(nil, Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty err = %v, want ErrInvalid", err)
	}
}

func TestExtract_OLE2IsUnsupported(t *testing.T) {
	if _, err := New().Extract([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1x"), Options{}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("OLE2 err = %v, want ErrUnsupported", err)
	}
}

func TestExtract_CorruptZipIsInvalid(t *testing.T) {
	// A zip magic with no valid central directory is corrupt, not merely unhandled.
	if _, err := New().Extract([]byte("PK\x03\x04garbage"), Options{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("corrupt zip err = %v, want ErrInvalid", err)
	}
}

func TestExtract_CSVSheets(t *testing.T) {
	data := []byte("Region,Stage,ACV\nEMEA,Negotiation,41200\nAPAC,Won,80000\n")
	res, err := New().Extract(data, Options{Filename: "pipeline.csv"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "csv" {
		t.Fatalf("format = %q, want csv", res.Format)
	}
	if len(res.Chunks) < 2 {
		t.Fatalf("want summary + data chunk, got %d", len(res.Chunks))
	}
	summary := res.Chunks[0]
	if !strings.Contains(summary.Text, "columns: Region, Stage, ACV") || !strings.Contains(summary.Text, "2 data rows") {
		t.Fatalf("summary chunk wrong: %q", summary.Text)
	}
	rows := res.Chunks[1]
	if !strings.Contains(rows.Text, "Region: EMEA | Stage: Negotiation | ACV: 41200") {
		t.Fatalf("row serialization wrong: %q", rows.Text)
	}
	if rows.Anchor.Kind != AnchorSheet || rows.Anchor.CellRange != "A2:C3" {
		t.Fatalf("sheet anchor wrong: %+v", rows.Anchor)
	}
}

func TestExtract_DocxProse(t *testing.T) {
	body := `<?xml version="1.0"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Onboarding Guide</w:t></w:r></w:p>
    <w:p><w:pPr><w:pStyle w:val="Heading2"/></w:pPr><w:r><w:t>Authentication</w:t></w:r></w:p>
    <w:p><w:r><w:t>Use </w:t></w:r><w:r><w:t>SSO to sign in.</w:t></w:r></w:p>
  </w:body>
</w:document>`
	res, err := New().Extract(makeDocx(t, body), Options{Filename: "guide.docx"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "docx" {
		t.Fatalf("format = %q, want docx", res.Format)
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d: %+v", len(res.Chunks), res.Chunks)
	}
	c := res.Chunks[0]
	if c.SourceLabel != "Onboarding Guide › Authentication" {
		t.Fatalf("breadcrumb label wrong: %q", c.SourceLabel)
	}
	if !strings.HasPrefix(c.Text, "Onboarding Guide › Authentication\n\n") || !strings.Contains(c.Text, "Use SSO to sign in.") {
		t.Fatalf("chunk text wrong: %q", c.Text)
	}
	if c.Anchor.Kind != AnchorSection || strings.Join(c.Anchor.HeadingPath, "/") != "Onboarding Guide/Authentication" {
		t.Fatalf("section anchor wrong: %+v", c.Anchor)
	}
}

func TestExtract_HTMLProse(t *testing.T) {
	data := []byte(`<html><head><style>x{}</style></head><body><h2>Auth</h2><p>Use <b>SSO</b> to sign in.</p></body></html>`)
	res, err := New().Extract(data, Options{Filename: "page.html"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "html" {
		t.Fatalf("format = %q, want html", res.Format)
	}
	if len(res.Chunks) != 1 {
		t.Fatalf("want 1 chunk, got %d", len(res.Chunks))
	}
	c := res.Chunks[0]
	if c.SourceLabel != "Auth" || !strings.Contains(c.Text, "Use SSO to sign in.") {
		t.Fatalf("html chunk wrong: label=%q text=%q", c.SourceLabel, c.Text)
	}
}

func TestExtract_XlsxSheets(t *testing.T) {
	f := excelize.NewFile()
	f.SetCellValue("Sheet1", "A1", "Region")
	f.SetCellValue("Sheet1", "B1", "ACV")
	f.SetCellValue("Sheet1", "A2", "EMEA")
	f.SetCellValue("Sheet1", "B2", 41200) // number -> computed value "41200", not a formula
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}
	_ = f.Close()

	res, err := New().Extract(buf.Bytes(), Options{Filename: "deals.xlsx"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "xlsx" {
		t.Fatalf("format = %q, want xlsx", res.Format)
	}
	if len(res.Chunks) < 2 {
		t.Fatalf("want summary + data chunk, got %d", len(res.Chunks))
	}
	if !strings.Contains(res.Chunks[0].Text, "columns: Region, ACV") {
		t.Fatalf("summary wrong: %q", res.Chunks[0].Text)
	}
	if !strings.Contains(res.Chunks[1].Text, "Region: EMEA | ACV: 41200") {
		t.Fatalf("row serialization wrong: %q", res.Chunks[1].Text)
	}
	if res.Chunks[1].Anchor.Sheet != "Sheet1" {
		t.Fatalf("sheet name wrong: %+v", res.Chunks[1].Anchor)
	}
}

func TestExtract_PptxPresentationOrder(t *testing.T) {
	// slide1.xml holds "Second", slide2.xml holds "First"; sldIdLst lists slide2's
	// rId before slide1's, so presentation order is First then Second — proving we
	// read sldIdLst, not the slideN.xml filename order (PRD §8).
	rels := `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
		`<Relationship Id="rIdA" Target="slides/slide1.xml"/>` +
		`<Relationship Id="rIdB" Target="slides/slide2.xml"/></Relationships>`
	pres := `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" ` +
		`xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">` +
		`<p:sldIdLst><p:sldId id="256" r:id="rIdB"/><p:sldId id="257" r:id="rIdA"/></p:sldIdLst></p:presentation>`
	data := makeZip(t, map[string]string{
		"[Content_Types].xml":             `<?xml version="1.0"?><Types/>`,
		"ppt/presentation.xml":            pres,
		"ppt/_rels/presentation.xml.rels": rels,
		"ppt/slides/slide1.xml":           `<p:sld xmlns:a="a"><a:t>Second slide</a:t></p:sld>`,
		"ppt/slides/slide2.xml":           `<p:sld xmlns:a="a"><a:t>First slide</a:t></p:sld>`,
	})

	res, err := New().Extract(data, Options{Filename: "deck.pptx"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "pptx" {
		t.Fatalf("format = %q, want pptx", res.Format)
	}
	if len(res.Chunks) == 0 {
		t.Fatal("want at least one slide chunk")
	}
	text := res.Chunks[0].Text
	fi, si := strings.Index(text, "First slide"), strings.Index(text, "Second slide")
	if fi < 0 || si < 0 || fi > si {
		t.Fatalf("slides not in presentation order: %q", text)
	}
	if res.Chunks[0].Anchor.Kind != AnchorSlide {
		t.Fatalf("slide anchor wrong: %+v", res.Chunks[0].Anchor)
	}
}

// makeDocx builds a minimal valid .docx (a zip with [Content_Types].xml and
// word/document.xml) so detection routes it to the docx extractor.
func makeDocx(t *testing.T, bodyXML string) []byte {
	t.Helper()
	return makeZip(t, map[string]string{
		"[Content_Types].xml": `<?xml version="1.0"?><Types/>`,
		"word/document.xml":   bodyXML,
	})
}

// makeZip builds an in-memory zip archive from name->content.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}
