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
	maxArchiveBytes  = 300 << 20 // aggregate uncompressed cap across a multi-part extraction (e.g. epub spine)
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
// zip-bomb entry can't exhaust memory. It rejects an entry whose central-
// directory-declared uncompressed size already exceeds the cap, and wraps the
// stream in an overflow-detecting reader so a lying/streamed size that inflates
// past the cap at read time surfaces as ErrInvalid rather than a silent
// truncation. The caller closes the result.
func openEntry(f *zip.File) (io.ReadCloser, error) {
	if f.UncompressedSize64 > maxZipEntryBytes {
		return nil, fmt.Errorf("%w: zip entry %q declares %d bytes (> %d cap)", ErrInvalid, f.Name, f.UncompressedSize64, maxZipEntryBytes)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	return boundedReadCloser{r: &overflowReader{r: rc, cap: maxZipEntryBytes, name: f.Name}, c: rc}, nil
}

type boundedReadCloser struct {
	r io.Reader
	c io.Closer
}

func (b boundedReadCloser) Read(p []byte) (int, error) { return b.r.Read(p) }
func (b boundedReadCloser) Close() error               { return b.c.Close() }

// overflowReader passes bytes through until the cumulative count exceeds cap, at
// which point it returns ErrInvalid — so a zip entry that decompresses past the
// cap fails loudly instead of being silently truncated (as io.LimitReader would).
type overflowReader struct {
	r    io.Reader
	cap  int64
	read int64
	name string
}

func (o *overflowReader) Read(p []byte) (int, error) {
	n, err := o.r.Read(p)
	o.read += int64(n)
	if o.read > o.cap {
		return n, fmt.Errorf("%w: zip entry %q exceeds %d bytes", ErrInvalid, o.name, o.cap)
	}
	return n, err
}

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
