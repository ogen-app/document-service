// Package server implements the documents.v1.DocumentsService gRPC service on
// top of the docengine extractor.
package server

import (
	"errors"
	"io"
	"log/slog"
	"runtime"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	documentsv1 "github.com/ogen-app/document-service/gen/documents/v1"
	"github.com/ogen-app/document-service/internal/docengine"
)

// maxDocBytes bounds the reassembled document held in memory. The Ogen API caps
// document uploads at 50 MB and the client at 128 MiB; allow headroom above that
// so an over-cap upload fails cleanly here rather than OOMing the pod.
const maxDocBytes = 150 << 20

// Server implements documentsv1.DocumentsServiceServer.
type Server struct {
	documentsv1.UnimplementedDocumentsServiceServer
	engine *docengine.Engine
	// sem bounds concurrent extractions (CPU-bound work); a burst of large uploads
	// queues rather than saturating the pod. Buffered to maxConcurrent.
	sem chan struct{}
}

// New builds a Server. maxConcurrent <= 0 resolves to GOMAXPROCS.
func New(engine *docengine.Engine, maxConcurrent int) *Server {
	if maxConcurrent <= 0 {
		maxConcurrent = runtime.GOMAXPROCS(0)
	}
	return &Server{engine: engine, sem: make(chan struct{}, maxConcurrent)}
}

// Parse reassembles the streamed document (bounded), extracts it into
// source-anchored chunks, and replies once. Terminal input errors surface as
// InvalidArgument/Unimplemented (the client must not retry); everything else is
// Internal (transient).
func (s *Server) Parse(stream documentsv1.DocumentsService_ParseServer) error {
	// Reassemble the upload FIRST (bounded by maxDocBytes) — do NOT hold an
	// extraction slot while waiting on a slow/idle uploader, or idle streams could
	// exhaust every slot.
	var opts *documentsv1.ParseOptions
	var data []byte
	for {
		req, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		switch p := req.Payload.(type) {
		case *documentsv1.ParseRequest_Options:
			opts = p.Options
		case *documentsv1.ParseRequest_Chunk:
			if len(data)+len(p.Chunk) > maxDocBytes {
				return status.Errorf(codes.InvalidArgument, "document exceeds %d bytes", maxDocBytes)
			}
			data = append(data, p.Chunk...)
		}
	}
	if opts == nil {
		opts = &documentsv1.ParseOptions{}
	}
	if len(data) == 0 {
		return status.Error(codes.InvalidArgument, "empty document")
	}

	// Now that the bytes are in hand, acquire an extraction slot (CPU-bound work);
	// respect client cancellation while queued.
	ctx := stream.Context()
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return status.FromContextError(ctx.Err()).Err()
	}

	// Pass the stream context so a client cancellation aborts the bounded parser
	// loops instead of running the extraction to completion for nobody.
	res, err := s.engine.ExtractContext(ctx, data, docengine.Options{
		Filename:     opts.GetFilename(),
		ContentType:  opts.GetContentType(),
		TargetChars:  int(opts.GetChunkTargetChars()),
		OverlapChars: int(opts.GetChunkOverlapChars()),
		MaxChars:     int(opts.GetChunkMaxChars()),
	})
	if err != nil {
		return mapEngineErr(err)
	}

	chunks := make([]*documentsv1.Chunk, len(res.Chunks))
	for i, c := range res.Chunks {
		chunks[i] = &documentsv1.Chunk{
			Index:       int32(c.Index),
			Text:        c.Text,
			SourceLabel: c.SourceLabel,
			Anchor:      toProtoAnchor(c.Anchor),
			TokenCount:  int32(c.TokenCount),
		}
	}
	slog.InfoContext(stream.Context(), "parse complete",
		"component", "server.parse",
		"format", res.Format,
		"chunks", len(chunks),
		"bytes", len(data),
	)
	return stream.SendAndClose(&documentsv1.ParseResponse{Format: res.Format, Chunks: chunks})
}

// toProtoAnchor maps the engine's Anchor to the proto Anchor.
func toProtoAnchor(a docengine.Anchor) *documentsv1.Anchor {
	return &documentsv1.Anchor{
		Kind:        toProtoAnchorKind(a.Kind),
		PageStart:   int32(a.PageStart),
		PageEnd:     int32(a.PageEnd),
		Slide:       int32(a.Slide),
		Sheet:       a.Sheet,
		CellRange:   a.CellRange,
		HeadingPath: a.HeadingPath,
	}
}

func toProtoAnchorKind(k docengine.AnchorKind) documentsv1.AnchorKind {
	switch k {
	case docengine.AnchorPage:
		return documentsv1.AnchorKind_ANCHOR_KIND_PAGE
	case docengine.AnchorSlide:
		return documentsv1.AnchorKind_ANCHOR_KIND_SLIDE
	case docengine.AnchorSheet:
		return documentsv1.AnchorKind_ANCHOR_KIND_SHEET
	case docengine.AnchorSection:
		return documentsv1.AnchorKind_ANCHOR_KIND_SECTION
	case docengine.AnchorEmail:
		return documentsv1.AnchorKind_ANCHOR_KIND_EMAIL
	default:
		return documentsv1.AnchorKind_ANCHOR_KIND_UNSPECIFIED
	}
}

// mapEngineErr turns a docengine sentinel into the right terminal gRPC code so
// the client classifies retry-vs-reject correctly; anything else is transient.
func mapEngineErr(err error) error {
	switch {
	case errors.Is(err, docengine.ErrUnsupported):
		return status.Error(codes.Unimplemented, err.Error())
	case errors.Is(err, docengine.ErrInvalid):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
