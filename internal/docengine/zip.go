package docengine

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// Zip-bomb bounds for the OOXML/ODF/EPUB families. A malicious archive can
// declare a tiny compressed size that inflates to gigabytes, so every entry is
// read through an io.LimitReader and the archive's entry count is capped (PRD
// §12). Detection reads only the tiny central directory + the `mimetype` member,
// so these bounds apply to extraction.
const (
	maxZipEntryBytes = 300 << 20 // per-entry uncompressed cap
	maxZipEntries    = 8192      // guards a directory of millions of tiny entries
)

// openZip parses the archive's central directory, mapping a corrupt archive or
// an absurd entry count to a terminal ErrInvalid.
func openZip(data []byte) (*zip.Reader, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: corrupt zip: %v", ErrInvalid, err)
	}
	if len(zr.File) > maxZipEntries {
		return nil, fmt.Errorf("%w: zip has too many entries (%d)", ErrInvalid, len(zr.File))
	}
	return zr, nil
}

// zipFile returns the named member, if present.
func zipFile(zr *zip.Reader, name string) (*zip.File, bool) {
	for _, f := range zr.File {
		if f.Name == name {
			return f, true
		}
	}
	return nil, false
}

// openEntry opens a member for streaming, bounded to maxZipEntryBytes so a
// zip-bomb entry can't exhaust memory. The caller closes the result.
func openEntry(f *zip.File) (io.ReadCloser, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return boundedReadCloser{r: io.LimitReader(rc, maxZipEntryBytes), c: rc}, nil
}

type boundedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (b boundedReadCloser) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b boundedReadCloser) Close() error               { return b.c.Close() }

// readZipEntry reads up to limit bytes of the named member as a string,
// best-effort (empty on any error). Used for the small `mimetype` sniff.
func readZipEntry(zr *zip.Reader, name string, limit int64) string {
	f, ok := zipFile(zr, name)
	if !ok {
		return ""
	}
	rc, err := f.Open()
	if err != nil {
		return ""
	}
	defer rc.Close()
	b, _ := io.ReadAll(io.LimitReader(rc, limit))
	return string(b)
}
