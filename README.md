# document-service

Internal gRPC microservice (CON-280) that parses office/text documents into
embedding-ready, **source-anchored** chunks. The direct sibling of
[`pdf-service`](../pdf-service) and [`video-service`](../video-service), minus
CGO (pure Go — no `libpdfium`) and minus rendering (no thumbnails in v1).

It serves `documents.v1.DocumentsService` — a single client-streaming `Parse`
RPC — plus a standard `grpc.health.v1` endpoint. The Ogen API streams document
bytes in, the service returns `[]Chunk` (each with a `source_label` + structured
`Anchor`), and Ogen embeds the chunks with its existing Gemini pipeline and
stores them in `assets_chunks`. Reached **only** over the Railway private network
(plaintext h2c, no public port).

## Contract

The `documents.v1` contract lives in the shared **`buf.build/ogen-app/proto`**
module (CON-220), not in this repo. Generated Go stubs are committed under `gen/`
so `go build` needs no `buf`; `make proto` regenerates them from a pinned module
version (`buf generate`). Ogen consumes the same contract via its own committed
`gen/documents/v1`, so both repos generate from the identical pinned module.

## Status

Implemented: magic-byte format detection and a per-family extraction engine that
normalises every format to a `Block` stream, then a per-shape anchored chunker
(prose heading-breadcrumb / spreadsheet labelled-fields + per-sheet summary /
atomic slides). Supported formats:

| Shape | Formats |
| -- | -- |
| Prose | `.docx`, `.odt`, `.epub`, `.html`/`.xhtml`, `.rtf`, `.txt`/`.log`, `.eml` |
| Spreadsheet | `.xlsx`, `.ods`, `.csv`/`.tsv` |
| Slides | `.pptx`, `.odp` |

Each chunk carries a `source_label` + structured `Anchor` (page/slide/sheet+cell
range/heading path/email). Legacy OLE2 binaries (`.doc/.xls/.ppt`) and unknown
formats return a terminal `Unimplemented`; corrupt/malformed input returns
`InvalidArgument`. Extraction streams the underlying XML and bounds zip
decompression so a hostile upload can't exhaust memory.

## Configuration

All settings are read from the environment (no prefix), matching the Ogen API's
style. The service listens on `DOCUMENTS_SERVICE_LISTEN`; the Ogen API dials it
via its own `DOCUMENTS_SERVICE_ADDR`.

| Env var | Default | Purpose |
| -- | -- | -- |
| `DOCUMENTS_SERVICE_LISTEN` | `:50051` | gRPC listen address (bare port allowed). |
| `DOCUMENTS_SERVICE_MAX_CONCURRENT` | `0` (→ GOMAXPROCS) | Concurrent extraction cap. |
| `DOCUMENTS_SERVICE_GC_PERCENT` | `50` | `GOGC` at boot. |
| `DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO` | `0.9` | `GOMEMLIMIT` as a fraction of the cgroup memory limit. |
| `LOG_LEVEL` | `info` | `debug`\|`info`\|`warn`\|`error`. |
| `LOG_FORMAT` | `json` | `json` (prod) or `text` (local). |

## Development

```sh
make proto   # buf lint + regenerate gen/
make build   # CGO_ENABLED=0 go build ./...
make test    # go test -race ./...
```
