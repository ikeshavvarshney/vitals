# vitals

Self-hosted Core Web Vitals monitoring in a single Go binary, with an empty
dependency manifest.

The tool that measures page weight should not be page weight. A mainstream
analytics snippet is 30-60KB of third-party JavaScript on every page view. This
one is **942 bytes**, served from your own origin: 7.7x smaller than Google's
`web-vitals`, and it includes the transport that `web-vitals` leaves to you. The
backend it talks to has no database server, no driver, no charting library, and
no framework.

```
$ cat go.mod
module vitals

go 1.23
```

That is the whole manifest.

**Zero Dependency Hackathon 2026, Track D (Data & Storage)**

---

## Try it in two minutes

```bash
git clone <repo> && cd vitals
make run
```

Open <http://localhost:8080/demo/> and click through a few pages. Then open
<http://localhost:8080/> for the dashboard. Numbers appear as the demo pages
report them.

The demo site has four pages, each broken in one specific way: a fast control,
one with a deliberately enormous inline hero image (poor LCP), one that inserts
unsized content on a timer (poor CLS), and one whose click handler blocks the
main thread (poor INP). Between them the scorecard shows all three status bands
rather than uniform green.

## Instrumenting your own site

Add one line to your pages, pointing at the origin where `vitals` is running:

```html
<script src="https://vitals.example.com/b.js" defer></script>
```

Locally that origin is `http://localhost:8080`. The script collects LCP, CLS,
INP, TTFB, and FCP, buffers them, and sends one small payload when the page is
hidden. It sets no cookie and no persistent identifier.

## What it does

- Collects the five Core Web Vitals with `PerformanceObserver`
- Ingests over HTTP, stores on local disk, survives restart
- Dashboard with p75 per metric, banded good / needs improvement / poor
- Time series and per-route breakdown
- One binary, one data directory, no external services

## Verifying zero dependencies

```bash
make proof     # writes deps-proof.txt
cat deps-proof.txt
```

`go list -m all` shows the main module and nothing else. `make check` fails the
build if a `require` block, a CDN reference, or a web font appears anywhere in
the repo.

## Reproducible build

```bash
make repro
```

Builds twice and prints both hashes.

```
SHA-256 (build 1): 0a2d61777f7b8e6c0e5dcadaefc0fa9470a87012545d02829860212ca832a1df
SHA-256 (build 2): 0a2d61777f7b8e6c0e5dcadaefc0fa9470a87012545d02829860212ca832a1df
```

Built with `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid="`. The
binary contains no build timestamp, git SHA, or injected version, which is what
makes the output byte-identical.

**What that hash is, precisely.** It is commit `bec6b84`, built on
`linux/amd64` with Go 1.23, by the `reproducible-build` job in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml), which builds twice and
runs `cmp` on the results. Every push reproduces it.

Reproducibility here means *the same source, toolchain, and target produce the
same bytes*. It does not mean one universal hash: a different Go version, a
different GOOS or GOARCH, or a different commit all produce a different and
equally valid digest. Building this commit on Windows, for instance, gives
`d5870efac8fe633560ac782e7a6304abcd2ac1dadb012a024d39c3f701106f33`. If you are
verifying, match your platform and Go version to the ones above, or simply run
`make repro` and check that your own two builds agree.

## Beacon size

Measured with `go run ./tools/compare`, which compresses every file with the
same gzip implementation. Comparing our file with one compressor and Google's
with another produces a difference of a few percent that is an artefact of the
tooling, so all three rows below come from one run.

| | Raw | Gzipped | vs ours (raw) | vs ours (gzip) |
|---|---|---|---|---|
| **`vitals` beacon** | **942 B** | **571 B** | - | - |
| `web-vitals` 4.2.4 `iife` | 7,226 B | 2,601 B | 7.7x | 4.6x |
| `web-vitals` 4.2.4 `attribution.iife` | 12,505 B | 4,172 B | 13.3x | 7.3x |

`make beacon` enforces the 1024-byte raw budget and fails the build if the
beacon exceeds it. To reproduce the comparison rows:

```bash
curl -O https://unpkg.com/web-vitals@4.2.4/dist/web-vitals.iife.js
go run ./tools/compare web-vitals.iife.js
```

**This is not quite a like-for-like comparison, and the difference favours us
unfairly in one respect and unfairly against us in another.**

Against us: `web-vitals` only measures. It hands each metric to a callback and
leaves transport entirely to you, so a real deployment adds your own reporting
code on top of the sizes above. Our 942 bytes already include JSON
serialisation, `sendBeacon`, the `fetch` fallback, and the flush-on-hide logic.

In our favour: `web-vitals` is doing more work per metric. It handles
back-forward cache restoration, prerendering and `activationStart`, soft
navigations, and years of accumulated browser quirks, none of which we do. Those
are not padding; they are the reason for most of the difference. The
`attribution` build is larger still because it also reports *which element*
caused a bad LCP or layout shift, which is genuinely useful and which we do not
attempt at all.

The honest summary: ours is smaller because it does less, and the things it
does not do are real. It is correct for a normal page load on current Chrome,
Firefox, and Safari, and that is the whole of the claim.

## Limits and honest notes

This section is the point of the project, so it is specific.

**INP is approximated.** True INP requires tracking full interaction latency
across all event entries and reporting a high percentile of them. This
implementation reports the **maximum duration of any single event** longer than
16ms, observed through `PerformanceObserver` with `durationThreshold: 16`. On a
page with few interactions the two agree. On a page with many, the maximum is
higher than the 98th percentile that real INP reports, so this number is
pessimistic and wrong in the tail. It is labelled INP on the dashboard because
that is the metric it approximates, and this paragraph is the correction.
Google's `web-vitals` does this properly.

**Percentiles are bucketed, not exact.** Values go into log-spaced histogram
buckets and p75 is read off cumulative counts. For the millisecond metrics the
buckets grow 10% at a time and the reported value is the geometric mean of its
bucket, which bounds the error at **4.9% relative**. CLS uses linear buckets of
0.005, so its error is 0.0025 absolute. Exact percentiles would require
retaining every sample in sorted order; this is the trade production RUM systems
make too.

**Up to 2 seconds of samples are lost on crash.** Writes are buffered and
flushed on an interval. For performance telemetry this is a deliberate trade,
not an oversight.

**The beacon handles fewer browser cases than `web-vitals`.** No back-forward
cache restoration handling, no soft-navigation support, less defensive coding
around older Safari. It is correct on current Chrome, Firefox, and Safari for a
normal page load.

**Storage is a single-node append log with an in-memory index.** No replication,
no compaction, no query planner. At the scale this targets (one site), that is
the right answer, not a compromise. It would not survive being pointed at a large
site.

**Secure context.** Some `PerformanceObserver` entry types and `sendBeacon`
behave differently on insecure origins. `localhost` counts as secure, so the
demo works; deploying to a plain-HTTP origin will lose metrics.

## What was replaced

See [`STDLIB.md`](STDLIB.md) for the full list: every package that would
normally be here, what replaced it, and where the original is better.

The short version: `express`, `serve-static`, `compression`, `etag`, and `cors`
became `internal/httpx`. `web-vitals` became a hand-written beacon. React, a
bundler, and a charting library became 402 lines of vanilla JS emitting inline
SVG. A database server and driver became an append log and a sorted slice.

## Build targets

| Command | Does |
|---|---|
| `make` | Build the binary |
| `make run` | Build and serve dashboard + demo on :8080 |
| `make test` | Run tests |
| `make race` | Run tests under the race detector, via Docker |
| `make check` | Fail on any dependency violation |
| `make proof` | Regenerate `deps-proof.txt` |
| `make repro` | Build twice, print both hashes |
| `make beacon` | Print beacon size, enforce the 1KB budget |
| `make compare` | Print beacon size beside another script for comparison |

## License

MIT. See [`LICENSE`](LICENSE).
