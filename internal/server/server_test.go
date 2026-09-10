package server

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	documentsv1 "github.com/ogen-app/document-service/gen/documents/v1"
	"github.com/ogen-app/document-service/internal/docengine"
)

// newTestClient starts the real Server over an in-memory bufconn and returns a
// generated client wired to it — a genuine end-to-end gRPC round-trip.
func newTestClient(t *testing.T) documentsv1.DocumentsServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	documentsv1.RegisterDocumentsServiceServer(srv, New(docengine.New(), 2))
	go func() { _ = srv.Serve(lis) }()

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(); srv.Stop() })
	return documentsv1.NewDocumentsServiceClient(conn)
}

// parse streams options + bytes and returns the single response.
func parse(t *testing.T, cl documentsv1.DocumentsServiceClient, filename string, data []byte) (*documentsv1.ParseResponse, error) {
	t.Helper()
	st, err := cl.Parse(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	if err := st.Send(&documentsv1.ParseRequest{Payload: &documentsv1.ParseRequest_Options{
		Options: &documentsv1.ParseOptions{Filename: filename},
	}}); err != nil {
		t.Fatalf("send options: %v", err)
	}
	if len(data) > 0 {
		if err := st.Send(&documentsv1.ParseRequest{Payload: &documentsv1.ParseRequest_Chunk{Chunk: data}}); err != nil {
			t.Fatalf("send bytes: %v", err)
		}
	}
	return st.CloseAndRecv()
}

func TestParse_TextSuccess(t *testing.T) {
	cl := newTestClient(t)
	resp, err := parse(t, cl, "notes.txt", []byte("First paragraph.\n\nSecond paragraph."))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if resp.GetFormat() != "txt" {
		t.Fatalf("format = %q, want txt", resp.GetFormat())
	}
	if len(resp.GetChunks()) == 0 {
		t.Fatal("want at least one chunk")
	}
	c := resp.GetChunks()[0]
	if c.GetText() == "" || c.GetAnchor().GetKind() != documentsv1.AnchorKind_ANCHOR_KIND_SECTION {
		t.Fatalf("unexpected chunk: %+v", c)
	}
}

func TestParse_EmptyIsInvalidArgument(t *testing.T) {
	cl := newTestClient(t)
	_, err := parse(t, cl, "empty.txt", nil)
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty document code = %v, want InvalidArgument", status.Code(err))
	}
}

func TestParse_OLE2IsUnimplemented(t *testing.T) {
	cl := newTestClient(t)
	_, err := parse(t, cl, "legacy.doc", []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1 body"))
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("OLE2 code = %v, want Unimplemented", status.Code(err))
	}
}
