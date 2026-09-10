package docengine

import (
	"strings"
	"testing"
)

func odfZip(t *testing.T, mimetype, contentXML string) []byte {
	return makeZip(t, map[string]string{
		"mimetype":    mimetype,
		"content.xml": contentXML,
	})
}

func TestExtract_OdtProse(t *testing.T) {
	content := `<office:document-content xmlns:office="o" xmlns:text="t">
<office:body><office:text>
<text:h text:outline-level="1">Onboarding</text:h>
<text:p>Welcome to the guide.</text:p>
</office:text></office:body></office:document-content>`
	res, err := New().Extract(odfZip(t, "application/vnd.oasis.opendocument.text", content), Options{})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "odt" {
		t.Fatalf("format = %q, want odt", res.Format)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].SourceLabel != "Onboarding" ||
		!strings.Contains(res.Chunks[0].Text, "Welcome to the guide.") {
		t.Fatalf("odt chunk wrong: %+v", res.Chunks)
	}
}

func TestExtract_OdsSheetsComputedValue(t *testing.T) {
	content := `<office:document-content xmlns:office="o" xmlns:table="ta" xmlns:text="t">
<office:body><office:spreadsheet>
<table:table table:name="Q3">
<table:table-row>
<table:table-cell office:value-type="string"><text:p>Region</text:p></table:table-cell>
<table:table-cell office:value-type="string"><text:p>ACV</text:p></table:table-cell>
</table:table-row>
<table:table-row>
<table:table-cell office:value-type="string"><text:p>EMEA</text:p></table:table-cell>
<table:table-cell office:value-type="float" office:value="41200"><text:p>41,200</text:p></table:table-cell>
</table:table-row>
</table:table></office:spreadsheet></office:body></office:document-content>`
	res, err := New().Extract(odfZip(t, "application/vnd.oasis.opendocument.spreadsheet", content), Options{})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "ods" {
		t.Fatalf("format = %q, want ods", res.Format)
	}
	if len(res.Chunks) < 2 {
		t.Fatalf("want summary + row chunk, got %d", len(res.Chunks))
	}
	// The float cell must serialize its computed value (41200), not the display
	// string "41,200".
	if !strings.Contains(res.Chunks[1].Text, "Region: EMEA | ACV: 41200") {
		t.Fatalf("computed value wrong: %q", res.Chunks[1].Text)
	}
	if res.Chunks[1].Anchor.Sheet != "Q3" {
		t.Fatalf("sheet name wrong: %+v", res.Chunks[1].Anchor)
	}
}

func TestExtract_OdpSlides(t *testing.T) {
	content := `<office:document-content xmlns:office="o" xmlns:draw="d" xmlns:text="t">
<office:body><office:presentation>
<draw:page draw:name="p1"><draw:frame><draw:text-box><text:p>Intro slide text</text:p></draw:text-box></draw:frame></draw:page>
</office:presentation></office:body></office:document-content>`
	res, err := New().Extract(odfZip(t, "application/vnd.oasis.opendocument.presentation", content), Options{})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "odp" {
		t.Fatalf("format = %q, want odp", res.Format)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].Anchor.Kind != AnchorSlide ||
		!strings.Contains(res.Chunks[0].Text, "Intro slide text") {
		t.Fatalf("odp chunk wrong: %+v", res.Chunks)
	}
}

func TestExtract_Epub(t *testing.T) {
	data := makeZip(t, map[string]string{
		"mimetype":               "application/epub+zip",
		"META-INF/container.xml": `<container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`,
		"OEBPS/content.opf":      `<package><manifest><item id="c1" href="chap1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
		"OEBPS/chap1.xhtml":      `<html><body><h1>Chapter One</h1><p>The body text.</p></body></html>`,
	})
	res, err := New().Extract(data, Options{})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "epub" {
		t.Fatalf("format = %q, want epub", res.Format)
	}
	if len(res.Chunks) != 1 || res.Chunks[0].SourceLabel != "Chapter One" ||
		!strings.Contains(res.Chunks[0].Text, "The body text.") {
		t.Fatalf("epub chunk wrong: %+v", res.Chunks)
	}
}

func TestExtract_EmlStripsQuoteAndSignature(t *testing.T) {
	raw := "From: a@b.com\nTo: c@d.com\nSubject: Hi\nContent-Type: text/plain\n\n" +
		"This is the real body.\n> some quoted reply\n-- \nMy Signature\n"
	res, err := New().Extract([]byte(raw), Options{Filename: "msg.eml"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "eml" {
		t.Fatalf("format = %q, want eml", res.Format)
	}
	joined := ""
	for _, c := range res.Chunks {
		joined += c.Text + "\n"
	}
	if !strings.Contains(joined, "This is the real body.") {
		t.Fatalf("body missing: %q", joined)
	}
	if strings.Contains(joined, "some quoted reply") || strings.Contains(joined, "My Signature") {
		t.Fatalf("quote/signature not stripped: %q", joined)
	}
	if res.Chunks[0].Anchor.Kind != AnchorEmail {
		t.Fatalf("email anchor wrong: %+v", res.Chunks[0].Anchor)
	}
}

func TestExtract_Rtf(t *testing.T) {
	raw := `{\rtf1\ansi\deff0 {\fonttbl{\f0 Arial;}}\f0\fs24 Hello \b world\b0\par Second line.\par}`
	res, err := New().Extract([]byte(raw), Options{Filename: "note.rtf"})
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if res.Format != "rtf" {
		t.Fatalf("format = %q, want rtf", res.Format)
	}
	joined := ""
	for _, c := range res.Chunks {
		joined += c.Text + "\n"
	}
	if !strings.Contains(joined, "Hello world") || !strings.Contains(joined, "Second line.") {
		t.Fatalf("rtf text wrong: %q", joined)
	}
	if strings.Contains(joined, "Arial") {
		t.Fatalf("font table leaked into text: %q", joined)
	}
}
