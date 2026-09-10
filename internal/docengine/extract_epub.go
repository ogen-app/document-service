package docengine

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

// extractEpub reads an EPUB's spine in reading order and extracts each XHTML
// chapter via the HTML extractor, concatenating the prose blocks. The spine order
// (not the zip order) is authoritative.
func extractEpub(data []byte) ([]Block, error) {
	zr, err := openZip(data)
	if err != nil {
		return nil, err
	}
	opfPath := epubOPFPath(zr)
	if opfPath == "" {
		return nil, fmt.Errorf("%w: epub missing OPF (META-INF/container.xml)", ErrInvalid)
	}
	manifest, spine := epubManifestSpine(zr, opfPath)
	baseDir := path.Dir(opfPath)

	var blocks []Block
	for _, idref := range spine {
		href, ok := manifest[idref]
		if !ok {
			continue
		}
		if i := strings.IndexByte(href, '#'); i >= 0 {
			href = href[:i]
		}
		part := resolveRel(baseDir, href)
		f, ok := zipFile(zr, part)
		if !ok {
			continue
		}
		rc, err := openEntry(f)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(rc)
		_ = rc.Close()
		chapter, err := extractHTML(body)
		if err != nil {
			continue // best-effort: skip an unparseable chapter
		}
		blocks = append(blocks, chapter...)
	}
	return blocks, nil
}

// epubOPFPath reads META-INF/container.xml for the OPF package path.
func epubOPFPath(zr *zip.Reader) string {
	f, ok := zipFile(zr, "META-INF/container.xml")
	if !ok {
		return ""
	}
	rc, err := openEntry(f)
	if err != nil {
		return ""
	}
	defer rc.Close()
	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "rootfile" {
			if p := attrValue(se, "full-path"); p != "" {
				return p
			}
		}
	}
	return ""
}

// epubManifestSpine parses the OPF into a manifest (id -> href) and the spine
// (ordered idrefs).
func epubManifestSpine(zr *zip.Reader, opfPath string) (map[string]string, []string) {
	manifest := map[string]string{}
	var spine []string
	f, ok := zipFile(zr, opfPath)
	if !ok {
		return manifest, spine
	}
	rc, err := openEntry(f)
	if err != nil {
		return manifest, spine
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) || err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "item":
			if id, href := attrValue(se, "id"), attrValue(se, "href"); id != "" && href != "" {
				manifest[id] = href
			}
		case "itemref":
			if idref := attrValue(se, "idref"); idref != "" {
				spine = append(spine, idref)
			}
		}
	}
	return manifest, spine
}
