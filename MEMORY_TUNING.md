# Memory tuning (Railway)

How to set the runtime-memory knobs for a **lightly-loaded** `document-service`
on Railway where **memory consumption translates directly to budget**.

## Billing model that drives these choices

Railway bills **actual measured usage** (GB-hours of real RSS + vCPU-hours), not
the plan cap or any limit you configure. A lightly-loaded service is idle for the
vast majority of its hours, so the **idle baseline dominates the bill** — not the
rare burst peak. The goal is therefore to keep idle RSS low, not just to cap the
peak.

The service holds no state across requests (every buffer is request-local and
bounded), so the "leak" seen in metrics is reclaimable memory the Go
runtime/cgroup hasn't returned — a flat plateau after a burst, not a rising
staircase. The knobs below make RSS track real usage.

## What actually holds memory here — one pool, fully governed

This is the key difference from `pdf-service` (native `libpdfium`) and
`video-service` (ffmpeg child processes): **document-service is pure Go with no
CGO and no child processes**, so *all* working memory is on the Go heap and every
knob below reaches *all* of it. There is no second, un-governed pool to reserve
headroom for.

What the heap holds, per in-flight `Parse`:

- the **reassembled document bytes** — `server.go` buffers the whole streamed
  upload before extraction, capped at `maxDocBytes` (150 MB; the ogen API caps
  real uploads at 50 MB);
- **per-format in-memory expansion** — the biggest driver. `.xlsx` (excelize)
  holds the workbook's shared strings + active sheet; `.html`/`.epub`
  (`x/net/html`) build a DOM; OOXML/ODF stream the XML token-by-token (bounded,
  `O(element)` — not the whole part) but the emitted **Block** stream and the
  **chunk** slices are held until the response is sent;
- gRPC framing buffers.

All request-local, all garbage once the RPC returns — and all counted and
bounded by `GOMEMLIMIT`. Zip decompression is bounded separately (per-entry
`io.LimitReader` at 300 MB + an 8192-entry cap) so a zip bomb can't blow the heap.

## Recommended values

```bash
DOCUMENTS_SERVICE_MAX_CONCURRENT=2         # the main peak / OOM lever
DOCUMENTS_SERVICE_MEMORY_LIMIT_RATIO=0.85  # a tight, effective ceiling on the WHOLE footprint
DOCUMENTS_SERVICE_GC_PERCENT=50            # keep default; minor lever here
```

### `MAX_CONCURRENT=2` — the lever that matters most

This bounds how many documents are extracted at once (default: `GOMAXPROCS`,
which on a multi-vCPU Railway box can be 4–8). Peak RSS is
`~(per-parse peak) × MAX_CONCURRENT`, and a per-parse peak is dominated by one
large `.xlsx`/`.docx`, so **capping concurrency is the most direct peak/OOM
reducer** — the analog of `VIDEO_SERVICE_WORKERS`. For a lightly-loaded service
where requests are rarely concurrent, **2** keeps the peak near a single parse at
negligible throughput cost; raise it only if you actually see queuing under load.

### `MEMORY_LIMIT_RATIO=0.85` — a real lever here, not just a safety net

This sets `GOMEMLIMIT` to a fraction of the container's cgroup memory limit. In
`pdf-service`/`video-service` this is kept **low (0.6–0.7)** to leave room for
their native/child memory that `GOMEMLIMIT` can't see. **Here there is no such
pool**, so the ratio can be **higher (0.85–0.9)** and it binds tightly on the
*entire* footprint. It does double duty:

1. a hard **ceiling** that prevents heap OOM under a burst, and
2. **scavenger pressure** — as the heap shrinks after a burst, the runtime
   scavenger returns freed pages to the OS (Linux/modern Go uses `MADV_DONTNEED`,
   so RSS drops promptly), keeping idle samples low.

Leave ~10–15 % for goroutine stacks, runtime structures, and OS overhead; don't
set it to 1.0.

| Your Railway memory limit | Suggested ratio | Reasoning |
|---|---|---|
| ≤ 512 MB | **0.8** | a little extra headroom so a big spreadsheet burst doesn't clip the ceiling |
| 1–4 GB (or unset) | **0.85–0.9** | ample; the ratio is the effective cap on all working memory |

If you haven't set an explicit memory limit on the Railway service, `memory.max`
is likely your plan's large default, so `0.9 × huge` is effectively unlimited and
the ratio does nothing. **Set an explicit Railway memory limit** (see sizing
below) — it costs nothing extra on usage-based billing, gives a hard backstop,
and makes the derived `GOMEMLIMIT` meaningful.

### `GC_PERCENT=50` — minor lever, don't overthink

GOGC only affects the heap high-water mark **during activity**. 50 gives slightly
lower burst peaks than the default 100 at trivial CPU cost. Don't go below ~40 —
that just burns CPU (also billed) chasing an already-small heap. It has little
effect on idle RSS once the workload drains; `GOMEMLIMIT` drives the idle reclaim.

## No forced idle-scavenge — and why that's fine here

Unlike `pdf-service`/`video-service`, this service has **no `SCAVENGE_ON_IDLE`
knob** (no `debug.FreeOSMemory` on drain). It doesn't need one: because
everything is Go-heap and `GOMEMLIMIT` is set, the runtime scavenger already
returns the post-burst plateau to the OS on its own, promptly, via
`MADV_DONTNEED`. If after deploy you observe idle RSS staying high for minutes
after a burst (it shouldn't), the cheapest fix is a **lower `GC_PERCENT`** to
shrink the heap goal; a forced idle-scavenge (mirroring the sibling services) is
the follow-up lever if that isn't enough — but measure first.

## Sizing the Railway memory limit

Idle baseline for a pure-Go gRPC service is small (~15–40 MB). Burst peak is
`(largest expected document's in-memory cost) × MAX_CONCURRENT`. For typical SMB
files (≤ a few MB) at `MAX_CONCURRENT=2`, **512 MB** is comfortable. If you expect
large (tens-of-MB) spreadsheets — excelize's workbook expansion is the worst case
— size to **1 GB** and keep `MEMORY_LIMIT_RATIO` at 0.85. Deploy, run a few real
files, and set the limit ~1.5–2× the observed peak.

## Two bigger levers

- **`MAX_CONCURRENT`** — above. The single biggest control over burst RSS and OOM
  risk on a bursty/idle service.
- **Input size / `maxDocBytes`** — the reassembled-bytes cap in `server.go` is
  150 MB, but the ogen API already caps uploads at 50 MB. If your real upload cap
  is smaller, lowering `maxDocBytes` to match tightens the worst-case per-parse
  buffer directly, more predictably than any GC knob.

## Verify after deploy

Run a few `Parse` calls (a large `.xlsx` and a `.docx` are the stress cases) and
confirm memory returns toward baseline between bursts (reuse) rather than climbing
across bursts (which would be a real leak). Boot logs the derived `GOMEMLIMIT`
under `component=runtimetune`; there is no per-scavenge log line (no forced
scavenge). At `LOG_LEVEL=debug`, each RPC logs its format, chunk count, and byte
size under `component=server.parse`, so you can correlate peak RSS with input.
