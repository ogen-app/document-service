package docengine

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strings"
)

// extractPptx parses a .pptx into slide blocks, one per slide in PRESENTATION
// order (read from ppt/presentation.xml's sldIdLst + rels — the slideN.xml
// filename order is not presentation order after edits, PRD §8). Each slide's
// visible text (all <a:t> runs) becomes a slide-body block; the slides chunker
// keeps slides atomic.
func extractPptx(data []byte) ([]Block, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	order := pptSlideOrder(zr)
	if len(order) == 0 {
		order = pptSlidesByName(zr) // fallback: filename order
	}
	var blocks []Block
	slideNum := 0
	for _, part := range order {
		text := pptSlideText(zr, part)
		slideNum++
		if strings.TrimSpace(text) == "" {
			continue
		}
		blocks = append(blocks, Block{Kind: BlockSlideBody, Text: text, Anchor: Anchor{Kind: AnchorSlide, Slide: slideNum}})
	}
	return blocks, nil
}

// pptSlideOrder resolves slide part names in presentation order via
// presentation.xml (sldIdLst r:id sequence) joined to presentation.xml.rels.
func pptSlideOrder(zr *zip.Reader) []string {
	rids := presentationSlideRIDs(zr)
	if len(rids) == 0 {
		return nil
	}
	rels := relationshipTargets(zr, "ppt/_rels/presentation.xml.rels")
	order := make([]string, 0, len(rids))
	for _, rid := range rids {
		if target, ok := rels[rid]; ok {
			order = append(order, resolveRel("ppt", target))
		}
	}
	return order
}

// presentationSlideRIDs returns the r:id of each slide in sldIdLst order.
func presentationSlideRIDs(zr *zip.Reader) []string {
	f, ok := zipFile(zr, "ppt/presentation.xml")
	if !ok {
		return nil
	}
	rc, err := openEntry(f)
	if err != nil {
		return nil
	}
	defer rc.Close()

	var rids []string
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "sldId" {
			if rid := relationshipAttr(se); rid != "" {
				rids = append(rids, rid)
			}
		}
	}
	return rids
}

// pptSlidesByName lists ppt/slides/slideN.xml entries sorted by N (fallback when
// the presentation order can't be read).
func pptSlidesByName(zr *zip.Reader) []string {
	var names []string
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			names = append(names, f.Name)
		}
	}
	sort.Slice(names, func(i, j int) bool { return slideOrdinal(names[i]) < slideOrdinal(names[j]) })
	return names
}

// pptSlideText concatenates every <a:t> run in a slide part.
func pptSlideText(zr *zip.Reader, part string) string {
	f, ok := zipFile(zr, part)
	if !ok {
		return ""
	}
	rc, err := openEntry(f)
	if err != nil {
		return ""
	}
	defer rc.Close()

	var b strings.Builder
	dec := xml.NewDecoder(rc)
	inText := false
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				b.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// ---- shared OOXML relationship helpers (also used by ODF/EPUB later) ----

// relationshipTargets parses a .rels part into an Id -> Target map.
func relationshipTargets(zr *zip.Reader, relsPart string) map[string]string {
	out := map[string]string{}
	f, ok := zipFile(zr, relsPart)
	if !ok {
		return out
	}
	rc, err := openEntry(f)
	if err != nil {
		return out
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			id, target := attrValue(se, "Id"), attrValue(se, "Target")
			if id != "" && target != "" {
				out[id] = target
			}
		}
	}
	return out
}

// relationshipAttr returns the value of the r:id attribute (the one in the
// officeDocument relationships namespace, distinct from the plain "id" attr).
func relationshipAttr(e xml.StartElement) string {
	for _, a := range e.Attr {
		if a.Name.Local == "id" && strings.Contains(a.Name.Space, "relationships") {
			return a.Value
		}
	}
	return ""
}

// resolveRel joins a relationship Target (relative to the part's base dir) into a
// full zip member name, resolving any "../".
func resolveRel(baseDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return strings.TrimPrefix(target, "/")
	}
	return path.Join(baseDir, target)
}

// slideOrdinal extracts the N from ".../slideN.xml" for filename-order sorting.
func slideOrdinal(name string) int {
	base := strings.TrimSuffix(path.Base(name), ".xml")
	base = strings.TrimPrefix(base, "slide")
	n := 0
	for _, r := range base {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
