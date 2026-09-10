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

The canonical proto lives here: `proto/documents/v1/documents.proto`. Generated
Go stubs are committed under `gen/` so `go build` needs no `buf`. Regenerate with
`make proto` (runs `buf lint` + `buf generate`). Ogen consumes the same contract
via its own committed `gen/documents/v1` (generated from this file); once
`documents.v1` is mirrored into `buf.build/ogen-app/proto` (CON-220), both repos
generate from the pinned module instead.

## Status

- **v0 (skeleton):** streaming server, health, structured logging, cgroup-aware
  runtime tuning, concurrency cap, and a working **plain-text** extractor. All
  other formats return a terminal `Unimplemented` today.
- **Next (the format matrix):** magic-byte detection + per-family extractors
  (OOXML `.docx/.pptx/.xlsx`, ODF, EPUB, CSV/TSV, HTML, EML, RTF) emitting a
  normalized `Block` stream, and a per-shape anchored chunker.

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
