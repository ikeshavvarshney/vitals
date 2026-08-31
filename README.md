# vitals

Self-hosted Core Web Vitals monitoring in a single Go binary, with an empty
dependency manifest.

The tool that measures page weight should not be page weight. A mainstream
analytics snippet is 30-60KB of third-party JavaScript on every page view. This
one is **942 bytes**, served from your own origin: 7.7x smaller than Google's
`web-vitals`, and it includes the transport that `web-vitals` leaves to you.
A second **2,656-byte** build is served alongside it for sites that want real
interaction-grouped INP, back-forward cache handling, soft navigations, and the
name of the element responsible: still 4.7x smaller than the `web-vitals` build
that does the same things. The backend they talk to has no database server, no
driver, no charting library, and no framework.

```
$ cat go.mod
module vitals

go 1.23
```

That is the whole manifest.

**Zero Dependency Hackathon 2026, Track D (Data & Storage)**

The visible surface is a dashboard, but the thing that was actually built is the
storage engine under it. Measurements land in an append-only JSONL log that
rotates on the UTC day boundary, with an in-memory index kept sorted by
timestamp so a time range is a binary search rather than a scan, and log-spaced
histograms that answer p75 without retaining a sample. Queries are served from
the buffer before it reaches disk, the log replays on startup and skips
corrupt lines rather than refusing to open, and retention drops whole day files
because rewriting an append log is how you corrupt one.
[`docs/storage.md`](docs/storage.md) states the durability and consistency
guarantees and where they are cut: writes are buffered, so a crash loses up to
2 seconds, and percentiles are bucketed to a 4.9% relative bound.
`TestSurvivesRestart`, `TestConcurrentAppendAndQuery`, and
`TestReplaySkipsCorruptLines` in
[`src/internal/store/store_test.go`](src/internal/store/store_test.go) hold
those claims up. This is the layer most analytics tools rent from Postgres or
ClickHouse; here it is 600 lines of `os`, `bufio`, and `encoding/json`.

---

## Try it in two minutes

```bash
git clone <repo> && cd vitals
make run
```

No `make` on your machine? The Go toolchain alone is enough, on every platform:

```bash
go run ./src/cmd/vitals
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

If the site is a single-page app, or you want to know *which* element is
responsible rather than only that a metric is bad, use the full build instead:

```html
<script src="https://vitals.example.com/b-full.js" defer></script>
```

It is 2,656 bytes and adds real interaction-grouped INP, back-forward cache
restores, soft navigations, prerender correction, and one element selector per
metric. Both post to the same endpoint and land in the same store, so a site can
run one on some pages and the other elsewhere. The demo site does exactly that:
the fast control page carries `/b.js` and the three broken pages carry
`/b-full.js`.

## Measuring a page you do not control

The beacon requires access to a site's HTML, which you will not have for a site
that is not yours. For those, the dashboard offers a bookmarklet.

Drag **vitals snapshot** from the dashboard to your bookmarks bar, open any
page, and click it. A small panel reports the page's vitals as the browser sees
them, and **Send to vitals** stores the measurement under a route named after
the page's host.

Nothing needs configuring. The bookmarklet cannot post to this server from the
page it measured, because Chrome blocks requests from a public page to a
loopback address, so it hands the payload to `/snapshot.html` through the URL
fragment and that page performs the same-origin POST. Fragments are never sent
to a server, so the measurement does not pass through anyone's access log on
the way.

What this is and is not:

- It is a real measurement. The numbers come from `PerformanceObserver` in your
  browser on the real page, replayed from its buffer rather than re-measured.
- It is **a sample of one**, taken on your machine and your connection. It is
  not that site's field data and it is not comparable to a `web.dev` score.
- INP only sees interactions that happen after the bookmarklet runs. A missing
  INP means unmeasured, not fast.
- LCP is final once a page has been interacted with, so click the bookmarklet
  before you click anything else on the page.

Verified against real sites in Chrome 152. Other browsers are untested.

## What it does

- Collects the five Core Web Vitals with `PerformanceObserver`
- Two beacons: 942 bytes for a normal page load, or 2,656 for real INP,
  back-forward cache, soft navigations, prerender correction, and element
  attribution
- Names the element blamed for a bad LCP, layout shift, or interaction
- **Visitor journeys**: one person's page views in order, so a visit that got
  worse as it went is visible rather than averaged away
- Ingests over HTTP, stores on local disk, survives a `kill -9` with at most two
  seconds lost, proven by a test that kills a real process
- Rate limited per source, because the collection endpoint is unauthenticated
  and writes to disk
- Dashboard with p50, p75, p90 or p95 per metric, banded good / needs
  improvement / poor
- Each figure compared with the window immediately before it, so a regression
  is visible rather than inferred
- Time series and per-route breakdown, with any route usable as a filter for
  the whole page
- Live updates over Server-Sent Events: the numbers move as visits arrive, with
  no polling
- Export a window as JSON, or as a prompt for an AI agent to read
- `vitals report` prints the same figures in a terminal, no browser needed
- Disk usage reported from the files, with optional day-granular retention
- One binary, one data directory, no external services

## Documentation

| Document | Contents |
|---|---|
| [`docs/architecture.md`](docs/architecture.md) | How the pieces fit together, request flow, design decisions, limits |
| [`docs/metrics.md`](docs/metrics.md) | What each Core Web Vital is, its thresholds, and how this project obtains it |
| [`docs/beacon.md`](docs/beacon.md) | What the client script measures, how accurate each metric is, what it does not handle |
| [`docs/api.md`](docs/api.md) | Every endpoint, parameter, and response shape |
| [`docs/storage.md`](docs/storage.md) | On-disk format, durability, percentile arithmetic and error bounds |
| [`docs/development.md`](docs/development.md) | Building, testing, CI, the dependency rules |
| [`tests/README.md`](tests/README.md) | What the black-box tests cover, and where the unit tests live |
| [`STDLIB.md`](STDLIB.md) | Every package replaced, and where the original is better |

## Repository layout

```
vitals/
├── README.md            what it does, how to run it, honest limits
├── STDLIB.md            every package replaced, and where the original is better
├── Makefile             one command to a runnable binary
├── go.mod               the manifest: no require block
├── deps-proof.txt       go.mod, go list -m all, go version
├── .zero-dep.toml       track letter and one-line pitch
├── .github/workflows/   CI: build, vet, test, race, dependency check, repro build
├── src/
│   ├── cmd/vitals/      the binary: flags, shutdown, logging, vitals report
│   ├── server/          the only exported package: Open, Handler, Report, Usage
│   ├── internal/        beacon, ingest, store, stats, dash, demo, httpx
│   └── tools/           dependency check, hashing, beacon size reporting
├── tests/               black-box tests over the served HTTP surface
└── docs/                architecture, metrics, beacon, API, storage, development
```

Two notes on where the tests are, since the layout above is not the whole
picture. `tests/` holds the black-box tests: they drive the binary's own routing
over HTTP and touch nothing but the public surface. Unit tests live beside the
code they cover, because Go compiles a package's tests from that package's own
directory, and only from there can they reach the unexported parsers and
arithmetic where the risk actually is. Moving them out would mean exporting
`parseQuery`, `summarize`, `wireRecord` and others for the sake of a directory
listing. [`tests/README.md`](tests/README.md) maps every layer to the tests that
cover it.

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

Builds twice and prints both hashes. From the CI run for commit `fe99284`,
Go 1.23.12, `linux/amd64`:

```
SHA-256 (build 1): d9ca445792027b83c9f24970902e607254fdcfadca933d023d71cb0792964c11
SHA-256 (build 2): d9ca445792027b83c9f24970902e607254fdcfadca933d023d71cb0792964c11
```

Because `-buildvcs=false` keeps the commit out of the binary, that digest holds
for any commit with the same Go source and embedded assets: a change to the
documentation does not alter it. A different Go version, a different
`GOOS`/`GOARCH`, or any change to the code produces a different and equally
valid digest, so the pair above is a claim about one build configuration rather
than a universal fingerprint. The run that produced it is linked from the
`reproducible-build` job of that commit.

Built with:

```
CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o vitals ./src/cmd/vitals
```

Each flag removes one source of variation. `-trimpath` strips local filesystem
paths. `-buildid=` clears the build ID. `-buildvcs=false` is the one that is
easy to miss: since Go 1.18 the toolchain stamps `vcs.revision`, `vcs.time`, and
a module pseudo-version into every binary built inside a git repository, so
without it the output changes on every commit even when no code changed. We got
this wrong at first and the README claimed a property the build did not have;
`go version -m vitals` now shows no `vcs.*` entries at all.

Nothing else injects a version, timestamp, or commit. If a version string is
ever wanted, it has to be a committed constant.

**What reproducibility means here.** The same source, toolchain, and target
produce the same bytes. It is not one universal hash: a different Go version, a
different GOOS or GOARCH, or a different commit that actually changes code all
produce a different and equally valid digest. The `reproducible-build` job in
[`.github/workflows/ci.yml`](.github/workflows/ci.yml) builds twice on
`linux/amd64` with Go 1.23 and runs `cmp` on the results, on every push, so the
claim is checked continuously rather than asserted once. To verify it yourself,
run `make repro` and confirm your own two builds agree.

## Beacon size

Measured with `go run ./src/tools/compare`, which compresses every file with the
same gzip implementation. Comparing our file with one compressor and Google's
with another produces a difference of a few percent that is an artefact of the
tooling, so all three rows below come from one run.

| | Raw | Gzipped | vs `/b.js` (raw) | vs `/b.js` (gzip) |
|---|---|---|---|---|
| **`vitals` beacon, `/b.js`** | **942 B** | **571 B** | - | - |
| **`vitals` full beacon, `/b-full.js`** | **2,656 B** | **1,415 B** | 2.8x | 2.5x |
| `web-vitals` 4.2.4 `iife` | 7,226 B | 2,601 B | 7.7x | 4.6x |
| `web-vitals` 4.2.4 `attribution.iife` | 12,505 B | 4,172 B | 13.3x | 7.3x |

The comparison that is actually like-for-like is row two against row four, the
two builds with attribution and real INP: **4.7x smaller raw, 2.9x gzipped**.
Row one against row three is the pair a site chooses between by default.

`make beacon` enforces a budget on each build and fails if either is exceeded:
1,024 raw bytes for `/b.js`, 2,816 for `/b-full.js`. Two budgets rather than one
raised budget, because the sub-1KB claim is about the script a site puts on
every page by default. To reproduce the comparison rows:

```bash
curl -O https://unpkg.com/web-vitals@4.2.4/dist/web-vitals.iife.js
go run ./src/tools/compare web-vitals.iife.js
```

**This is not quite a like-for-like comparison, and the difference favours us
unfairly in one respect and unfairly against us in another.**

Against us: `web-vitals` only measures. It hands each metric to a callback and
leaves transport entirely to you, so a real deployment adds your own reporting
code on top of the sizes above. Our 942 bytes already include JSON
serialisation, `sendBeacon`, the `fetch` fallback, and the flush-on-hide logic.

In favour of `/b.js`: `web-vitals` is doing more work per metric. It handles
back-forward cache restoration, prerendering and `activationStart`, soft
navigations, and years of accumulated browser quirks, none of which the 942-byte
build does. Those are not padding; they are the reason for most of the
difference.

`/b-full.js` exists to make that comparison fair. It does the first four of
those, reports real interaction-grouped INP, and names the element responsible,
in 2,656 bytes against the 12,505 of the `attribution` build. What it still does
not do is the browser-quirk work, the subpart timing breakdown of an
interaction, or long-animation-frame data.

The honest summary: `/b.js` is smaller because it does less, and the things it
does not do are real. `/b-full.js` does nearly all of them, and its own gap is
that no browser has yet exercised those paths in the field. See
[`docs/beacon.md`](docs/beacon.md#9-verification-status).

## Limits and honest notes

This section is the point of the project, so it is specific.

**INP is approximated by `/b.js`, and real in `/b-full.js`.** See
[`docs/beacon.md`](docs/beacon.md#4-what-is-collected-and-how-accurate-it-is).
True INP requires tracking full interaction latency across all event entries and
reporting a high percentile of them. The 942-byte beacon reports the **maximum
duration of any single event** longer than 16ms. On a page with few interactions
the two agree. On a page with many, the maximum is higher than the 98th
percentile that real INP reports, so that number is pessimistic and wrong in the
tail. The full beacon groups entries by `interactionId` and applies the
specification's percentile rule, so its figure is the real one.

**A window can mix the two.** Both beacons store INP under the same key and
which beacon sent a sample is not recorded, so a site running both has a window
containing an approximation and a real figure side by side. The report says so
in its caveats. Recording it would mean a per-record field to untangle something
that only matters while a site is migrating between the two.

**Percentiles are bucketed, not exact.** Full arithmetic in [`docs/storage.md`](docs/storage.md#3-percentiles). Values go into log-spaced histogram
buckets and p75 is read off cumulative counts. For the millisecond metrics the
buckets grow 10% at a time and the reported value is the geometric mean of its
bucket, which bounds the error at **4.9% relative**. CLS uses linear buckets of
0.005, so its error is 0.0025 absolute. Exact percentiles would require
retaining every sample in sorted order; this is the trade production RUM systems
make too.

**Up to 2 seconds of samples are lost on crash.** Writes are buffered and
flushed on an interval. For performance telemetry this is a deliberate trade,
not an oversight.

**The full beacon's own paths are untested in a browser.** Its server half is
covered end to end, from the payload parser through to the rendered report, and
its syntax is checked. What has not happened is a real browser exercising a
bfcache restore, a soft navigation, a prerendered page, or an interaction
grouped by `interactionId`. Those are written against the specifications and
reviewed, not demonstrated. `/b.js` has been driven end to end in Chrome 152.
This is the largest honest gap in the project and it is why `/b.js` remains the
default.

**Both beacons handle fewer browser quirks than `web-vitals`.** Years of
accumulated workarounds, particularly for older Safari, are absent. `/b.js`
additionally has no back-forward cache handling, no soft-navigation support, no
prerender correction, and does not discard a paint reported after the page was
first hidden, so a page opened in a background tab contributes an LCP nobody
saw. `/b-full.js` does all four.

**Element attribution is a selector, not a diagnosis.** The full beacon names
one element per metric as a tag plus an id or first class. It is not a unique
path, so identical sibling elements are counted as one, and it says which
element rather than why it was slow. The `web-vitals` attribution build reports
the subpart timing breakdown of an interaction and long-animation-frame data;
this does not attempt either.

**Retention deletes whole days.** With `-retain` set, expiry removes an entire
day log at a time and never rewrites a file, because a partially rewritten
append log is a corrupt one. A day is kept until every record it could hold is
older than the window, so the tool keeps slightly more than asked rather than
slightly less. Without the flag nothing is deleted and `data/` grows forever.

**Live updates carry no figures.** The event stream sends a route and a
timestamp; the dashboard re-reads the API. A dashboard that has fallen more than
eight notifications behind starts missing them and catches up on the next one,
which is invisible in practice because a notification only means "re-read".

**Storage is a single-node append log with an in-memory index.** No replication,
no compaction, no query planner. At the scale this targets (one site), that is
the right answer, not a compromise. It would not survive being pointed at a large
site, and the limit has a number: replaying 100,000 records takes **593ms** and
allocates 164MB, so a million records is roughly six seconds and 1.6GB to open.
About 500ms of that 593 is `encoding/json` on the read path. The full table is in
[`docs/storage.md`](docs/storage.md#2b-measured-cost).

**A visitor identifier is not a person.** The journeys view follows one visitor
through a session, which looks like tracking and is not: the identifier is a
truncated hash of the request origin, the user agent, and the current UTC date.
It rotates at midnight, is never stored in a cookie, and cannot be linked to the
same person tomorrow or on another site. The consequence is that a journey never
spans midnight UTC, and two visitors behind the same address with the same
browser are counted as one.

**The rate limit is per address, and a reverse proxy defeats it.** Forwarded
headers are ignored because they are trivially spoofed, so behind a proxy every
visitor shares one bucket. Run with `-rate -1` and limit at the proxy instead.

**The binary segment format was planned and deliberately cut.** The design
called for compacting old day logs into a hand-written binary format with a
fixed record layout and a CRC per segment, replacing JSONL on disk and removing
`encoding/json` from the write path. It is not here. Cutting it was a decision
made in advance rather than an omission discovered at the end: a new on-disk
format is the one change in this project that can silently lose data, and it
would have landed without time to soak.

What that costs: a record occupies about 150 bytes as JSONL, which is measured
from this project's own data directory and reported live in the dashboard's
storage panel. A packed binary record carrying the same fields would be roughly
40 to 60 bytes, which is an estimate rather than a measurement because the
format was never built. Call it two to three times larger than it needs to be,
plus a JSON parse per record on every restart instead of a fixed-width read.

What it buys: the on-disk format is a text file you can `tail`, `grep`, and
repair by hand, a corrupt line costs one measurement rather than a whole
segment, and the durability behaviour is the one already exercised by tests
rather than newly written code. Retention takes the same shape for the same
reason: whole day files expire, and no file is ever rewritten in place.

**Secure context.** Some `PerformanceObserver` entry types and `sendBeacon`
behave differently on insecure origins. `localhost` counts as secure, so the
demo works; deploying to a plain-HTTP origin will lose metrics.

**Verified in a real browser.** The beacon has been driven end to end in
Chrome 152 over the DevTools Protocol: all four demo pages loaded with no
console errors, uncaught exceptions, or failed requests; all five metrics were
collected and ingested; and the dashboard rendered the scorecard, chart, and
tables from that data. INP was measured at 600.0ms against a handler
deliberately blocking for 600ms. Firefox and Safari have **not** been tested,
so support there is claimed on standards compliance rather than on evidence.

## What was replaced

See [`STDLIB.md`](STDLIB.md) for the full list: 45 entries, each naming the
package that would normally be here, what replaced it, and where the original
is better.

The short version: `express`, `serve-static`, `compression`, `etag`, and `cors`
became `src/internal/httpx`. `web-vitals` became a hand-written beacon, and
`web-vitals/attribution` a second one. `lru-cache` became a map beside a fixed
ring. `express-rate-limit`, and `golang.org/x/time/rate` which the event rules
ban anyway, became a token bucket per address. React, a bundler, and a charting
library became vanilla JS emitting inline SVG. A database server and driver
became an append log and a sorted slice.

## Build targets

Every target wraps one Go command, so `make` is a convenience and never a
requirement. The last column is what to run without it.

| Command | Does | Without make |
|---|---|---|
| `make` | Build the binary | `go build -o vitals ./src/cmd/vitals` |
| `make run` | Build and serve dashboard + demo on :8080 | `go run ./src/cmd/vitals` |
| `make report` | Print the last 24 hours in the terminal | `go run ./src/cmd/vitals report -window 24h` |
| `make test` | Run tests | `go test ./...` |
| `make bench` | Print the storage benchmarks | `go test ./src/internal/store/ -bench . -benchmem -run '^$'` |
| `make race` | Run tests under the race detector, via Docker | `go test -race ./...` |
| `make check` | Fail on any dependency violation | `go run ./src/tools/check .` |
| `make proof` | Regenerate `deps-proof.txt` | `go list -m all` |
| `make repro` | Build twice, print both hashes | `go run ./src/tools/sha256sum <files>` |
| `make beacon` | Print beacon size, enforce the 1KB budget | `go run ./src/tools/beaconsize` |
| `make compare` | Print beacon size beside another script for comparison | `go run ./src/tools/compare <files>` |
| `make clean` | Remove build output | `rm -f vitals vitals.exe vitals.repro1 vitals.repro2` |

Verified on Windows with GNU Make 3.81 and on Linux with GNU Make 4.x. The only
target that needs anything outside the Go toolchain is `make race`, which
borrows Linux's C toolchain through Docker because the race detector needs an
external linker; CI runs `go test -race` natively instead.

## License

MIT. See [`LICENSE`](LICENSE).
