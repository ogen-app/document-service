package docengine

import (
	"archive/zip"
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// family is the resolved document family driving extractor + shape selection.
type family int

const (
	familyText family = iota // txt/log
	familyCSV
	familyTSV
	familyHTML
	familyDOCX
	familyXLSX
	familyPPTX
	familyODT
	familyODS
	familyODP
	familyEPUB
	familyEML
	familyRTF
)

// Magic-byte signatures. Detection is by content, never by the (attacker-
// controlled) extension or client Content-Type — those are only a tiebreaker
// among the text subtypes, which are otherwise indistinguishable.
var (
	sigZip  = []byte("PK\x03\x04")
	sigOLE2 = []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")
	sigRTF  = []byte(`{\rtf`)
)

// detect classifies data by its leading bytes (authoritative for the zip and
// OLE2 container families) and, for the text family, by the advisory hint
// (extension or MIME) with a light content sniff fallback.
func detect(data []byte, hint string) (format string, fam family, err error) {
	if len(data) == 0 {
		return "", 0, fmt.Errorf("%w: empty payload", ErrInvalid)
	}
	switch {
	case bytes.HasPrefix(data, sigOLE2):
		return "", 0, fmt.Errorf("%w: legacy OLE2 binary (.doc/.xls/.ppt) or encrypted Office file", ErrUnsupported)
	case bytes.HasPrefix(data, sigZip):
		return detectZip(data)
	case bytes.HasPrefix(data, sigRTF):
		return "rtf", familyRTF, nil
	default:
		return detectText(data, hint)
	}
}

// detectZip opens the archive and discriminates OOXML vs ODF vs EPUB by their
// well-known member entries. It reads only the tiny central directory + the
// small `mimetype` member, so it is cheap; the extractor enforces the zip-bomb
// bounds on the full decompression.
func detectZip(data []byte) (string, family, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, fmt.Errorf("%w: corrupt zip container: %v", ErrInvalid, err)
	}
	names := make(map[string]bool, len(zr.File))
	hasPrefix := func(p string) bool {
		for n := range names {
			if strings.HasPrefix(n, p) {
				return true
			}
		}
		return false
	}
	for _, f := range zr.File {
		names[f.Name] = true
	}

	// ODF and EPUB declare themselves in a `mimetype` member.
	if names["mimetype"] {
		if mt := readZipEntry(zr, "mimetype", 256); mt != "" {
			switch strings.TrimSpace(mt) {
			case "application/epub+zip":
				return "epub", familyEPUB, nil
			case "application/vnd.oasis.opendocument.text":
				return "odt", familyODT, nil
			case "application/vnd.oasis.opendocument.spreadsheet":
				return "ods", familyODS, nil
			case "application/vnd.oasis.opendocument.presentation":
				return "odp", familyODP, nil
			}
		}
	}

	// OOXML has a [Content_Types].xml plus a family-named part directory.
	if names["[Content_Types].xml"] {
		switch {
		case hasPrefix("word/"):
			return "docx", familyDOCX, nil
		case hasPrefix("xl/"):
			return "xlsx", familyXLSX, nil
		case hasPrefix("ppt/"):
			return "pptx", familyPPTX, nil
		}
	}

	if names["META-INF/container.xml"] {
		return "epub", familyEPUB, nil
	}
	return "", 0, fmt.Errorf("%w: unrecognised zip-based document", ErrUnsupported)
}

// detectText resolves a text-family subtype. The extension/MIME hint decides
// (a .csv and a .txt are indistinguishable by content); a missing hint falls back
// to an HTML sniff, else plain text.
func detectText(data []byte, hint string) (string, family, error) {
	switch hint {
	case ".csv":
		return "csv", familyCSV, nil
	case ".tsv":
		return "tsv", familyTSV, nil
	case ".html", ".htm", ".xhtml":
		return "html", familyHTML, nil
	case ".eml":
		return "eml", familyEML, nil
	case ".rtf":
		return "rtf", familyRTF, nil
	case ".txt", ".log":
		return "txt", familyText, nil
	}
	if looksHTML(data) {
		return "html", familyHTML, nil
	}
	return "txt", familyText, nil
}

// looksHTML reports whether the head of data appears to be HTML.
func looksHTML(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	l := strings.ToLower(string(head))
	return strings.Contains(l, "<!doctype html") || strings.Contains(l, "<html") || strings.Contains(l, "<body")
}

// hintFrom derives the text-subtype hint from the advisory filename extension,
// falling back to the client Content-Type.
func hintFrom(opts Options) string {
	if opts.Filename != "" {
		if ext := strings.ToLower(filepath.Ext(opts.Filename)); ext != "" {
			return ext
		}
	}
	switch {
	case strings.Contains(opts.ContentType, "csv"):
		return ".csv"
	case strings.Contains(opts.ContentType, "tab-separated"):
		return ".tsv"
	case strings.Contains(opts.ContentType, "html"):
		return ".html"
	case strings.Contains(opts.ContentType, "rfc822"):
		return ".eml"
	case strings.Contains(opts.ContentType, "rtf"):
		return ".rtf"
	}
	return ""
}
