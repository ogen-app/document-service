// Package docengine detects a document's format and extracts it into
// embedding-ready, source-anchored chunks. It is the pure-Go analog of
// pdf-service's pdfengine (no CGO): format detection is by magic bytes, each
// extractor emits a normalized Block stream (never a pre-flattened string), and
// one chunker (chunk.go) turns those blocks into anchored chunks per shape.
//
// Detection is authoritative for the zip (OOXML/ODF/EPUB) and OLE2 container
// families; the text subtypes (csv/tsv/html/eml/rtf/txt) fall back to the
// advisory filename/Content-Type hint. Extraction streams the underlying XML and
// bounds decompression so a hostile upload can't OOM the pod (PRD §12).
package docengine

import "fmt"

// Sentinel errors classify a failure as terminal at the gRPC boundary (see the
// server's mapEngineErr). ErrUnsupported -> Unimplemented (convert the file);
// ErrInvalid -> InvalidArgument (corrupt/empty/malformed). Anything else is
// transient (Internal) and the client retries.
var (
	// ErrUnsupported means the format is recognised-but-unhandled (legacy OLE2
	// binary, iWork) or unrecognised — the caller must convert and re-upload.
	ErrUnsupported = fmt.Errorf("docengine: unsupported document format")
	// ErrInvalid means the bytes are the right shape but unusable: empty, corrupt,
	// encrypted, or malformed.
	ErrInvalid = fmt.Errorf("docengine: invalid document")
)

// AnchorKind mirrors documents.v1.AnchorKind; the server maps it to the proto
// enum. Kept engine-local so docengine has no proto dependency.
type AnchorKind int

const (
	AnchorUnspecified AnchorKind = iota
	AnchorPage
	AnchorSlide
	AnchorSheet
	AnchorSection
	AnchorEmail
)

// Anchor is the structured source location of a chunk. Which fields are set
// depends on Kind (see the per-format extractors and the chunker).
type Anchor struct {
	Kind        AnchorKind
	PageStart   int
	PageEnd     int
	Slide       int
	Sheet       string
	CellRange   string
	HeadingPath []string
}

// Chunk is one embedding-ready piece of a document. Text already carries any
// breadcrumb / sheet-label prefix and is guaranteed valid UTF-8.
type Chunk struct {
	Index       int
	Text        string
	SourceLabel string
	Anchor      Anchor
	TokenCount  int
}

// Result is a parsed document: the canonical detected format plus its chunks.
type Result struct {
	Format string
	Chunks []Chunk
}

// Options controls extraction and chunk sizing. Filename/ContentType are advisory
// hints used only to disambiguate the text subtypes; zero sizing values fall back
// to the engine defaults.
type Options struct {
	Filename     string
	ContentType  string
	TargetChars  int
	OverlapChars int
	MaxChars     int
}

// Default chunk sizing (chars), tuned to the ~512-1500-token prose target. These
// mirror pdf-service's defaults; ParseOptions overrides them per call.
const (
	defaultTargetChars = 5500
	defaultMaxChars    = 6000
)

// Engine extracts documents. It is stateless and safe for concurrent use; the
// struct exists so future per-instance state (buffer pools) has a home without a
// signature change, mirroring pdfengine.
type Engine struct{}

// New constructs an Engine.
func New() *Engine { return &Engine{} }

// Extract detects the format of data and returns its anchored chunks. It never
// panics on hostile input: unrecognised/unhandled formats return ErrUnsupported,
// empty/corrupt input returns ErrInvalid.
func (e *Engine) Extract(data []byte, opts Options) (*Result, error) {
	format, fam, err := detect(data, hintFrom(opts))
	if err != nil {
		return nil, err
	}
	sh, blocks, err := extract(fam, format, data)
	if err != nil {
		return nil, err
	}
	return &Result{Format: format, Chunks: chunkBlocks(sh, blocks, opts)}, nil
}

// extract dispatches to the family's extractor, returning the document shape and
// its Block stream. Detected-but-unimplemented families return a precise
// ErrUnsupported so the message names the format.
func extract(fam family, format string, data []byte) (shape, []Block, error) {
	switch fam {
	case familyText:
		return shapeProse, extractText(data), nil
	case familyCSV:
		return shapeSheets, extractDelimited(data, ','), nil
	case familyTSV:
		return shapeSheets, extractDelimited(data, '\t'), nil
	case familyHTML:
		blocks, err := extractHTML(data)
		return shapeProse, blocks, err
	case familyDOCX:
		blocks, err := extractDocx(data)
		return shapeProse, blocks, err
	case familyXLSX:
		blocks, err := extractXlsx(data)
		return shapeSheets, blocks, err
	case familyPPTX:
		blocks, err := extractPptx(data)
		return shapeSlides, blocks, err
	case familyODT:
		blocks, err := extractOdt(data)
		return shapeProse, blocks, err
	case familyODS:
		blocks, err := extractOds(data)
		return shapeSheets, blocks, err
	case familyODP:
		blocks, err := extractOdp(data)
		return shapeSlides, blocks, err
	case familyEPUB:
		blocks, err := extractEpub(data)
		return shapeProse, blocks, err
	case familyEML:
		blocks, err := extractEml(data)
		return shapeProse, blocks, err
	case familyRTF:
		blocks, err := extractRtf(data)
		return shapeProse, blocks, err
	default:
		return 0, nil, fmt.Errorf("%w: %s extraction not yet implemented", ErrUnsupported, format)
	}
}

// estimateTokens mirrors the ogen consumer's ≈4-chars/token heuristic so the
// service can populate Chunk.TokenCount; the consumer treats 0 as "count it
// yourself".
func estimateTokens(s string) int {
	if len(s) == 0 {
		return 0
	}
	if t := len(s) / 4; t > 0 {
		return t
	}
	return 1
}
