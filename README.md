# vitals

Self-hosted Core Web Vitals monitoring in a single Go binary, with an empty
dependency manifest.

The tool that measures page weight should not be page weight. A mainstream
analytics snippet is 30-60KB of third-party JavaScript on every page view. This
one is **[XXX] bytes**, served from your own origin, and the backend it talks to
has no database server, no driver, no charting library, and no framework.

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

The demo site has pages of deliberately varied performance (a fast one, one
with a large hero image, and one that shifts layout after load), so the
scorecard shows all three status bands rather than uniform green.

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
SHA-256 (build 1): [XXX]
SHA-256 (build 2): [XXX]
```

Built with `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid="`. The
binary contains no build timestamp, git SHA, or injected version, which is what
makes the output byte-identical.

## Beacon size

| | Raw | Gzipped |
|---|---|---|
| `vitals` beacon | **[XXX] B** | [XXX] B |
| Google `web-vitals` v4 (UMD) | ~[XXX] B | ~[XXX] B |

Measured with `make beacon`, which fails the build if the raw beacon exceeds
1024 bytes.

The beacon exists in two forms: `beacon.src.js` is the readable, commented
source, and `beacon.min.js` is a hand-minified version. There is no minifier
here to run, so it was minified by hand. Review the source; the minified file is
mechanically derived from it.

## Limits and honest notes

This section is the point of the project, so it is specific.

**INP is approximated.** True INP requires tracking interaction latency across
all event entries and taking a high percentile of them. This implementation
[describe what it actually does]. It is directionally correct and wrong in the
tail. Google's `web-vitals` does this properly.

**Percentiles are bucketed, not exact.** Values go into log-spaced histogram
buckets and p75 is read off cumulative counts. Error is bounded by bucket
width: under [XXX]% relative. Exact percentiles would require retaining every sample
in sorted order; this is the trade production RUM systems make too.

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
bundler, and a charting library became [XXX] lines of vanilla JS emitting inline
SVG. A database server and driver became an append log and a sorted slice.

## Documents

| File | Contents |
|---|---|
| [`PRD.md`](PRD.md) | Scope, priorities, and the cut lines decided in advance |
| [`DESIGN.md`](DESIGN.md) | Architecture, storage format, statistics, and JSON API |
| [`STDLIB.md`](STDLIB.md) | Every package replaced, and where the original is better |
| [`CLAUDE.md`](CLAUDE.md) | The dependency rules this repository is built under |

## Build targets

| Command | Does |
|---|---|
| `make` | Build the binary |
| `make run` | Build and serve dashboard + demo on :8080 |
| `make test` | Run tests |
| `make check` | Fail on any dependency violation |
| `make proof` | Regenerate `deps-proof.txt` |
| `make repro` | Build twice, print both hashes |
| `make beacon` | Minify beacon, print size, enforce 1KB budget |

## License

MIT. See [`LICENSE`](LICENSE).
