# STDLIB.md

Every package that would normally be in this project, what replaced it, and
where the replaced package is genuinely better.

Entries are appended at the moment the decision is made, not reconstructed at
the end. An entry is only written for a package we would actually have reached
for.

---

## Build and tooling

- **`cobra` / `viper` / `commander`** → `flag` from the standard library in
  `cmd/vitals/main.go`. Two flags, `-addr` and `-data`, need two lines. Cobra
  gives subcommands, shell completion, config-file merging, and environment
  binding, none of which this binary has any use for. If the tool ever grew a
  real command tree, `flag` would start to hurt.

- **`ory/dockertest` / `testcontainers-go` / a shell script around the binary**
  → `tests/crash_test.go`, using `os/exec` and `net/http` from the standard
  library. It builds the binary with `go build`, starts it as a child process on
  a port reserved through `net.Listen`, posts real measurements over HTTP, kills
  it with `Process.Kill`, and starts a second one on the same data directory.

  This is how the durability claim is tested rather than asserted: a clean
  `Close` proves nothing about what a `kill -9` costs. Where the packages are
  better: they manage container lifecycles, networks, and cleanup across
  platforms, and they would let the same test run against a server on another
  machine. This runs one local process and knows nothing about containers.

- **`jest` / `mocha` / `testify` / `gocheck`** → `testing` from the standard
  library, table-driven throughout. Go is unusual in shipping a test runner, so
  this avoids the development-dependency grey area entirely: there is no
  `devDependencies` section to argue about. `testify` has nicer assertion
  failure output and a mocking package; hand-written table tests print less
  helpful diffs on failure.

- **`sha256sum` / `shasum` / `certutil` / `hasha`** → `tools/sha256sum`, 40
  lines over `crypto/sha256`. Shelling out to a checksum utility would make the
  reproducible-build proof depend on which platform the judge is running:
  `sha256sum` on Linux, `shasum` on macOS, `certutil` on Windows. The real
  utilities handle streaming from stdin, BSD-style output, and `--check`
  verification against a manifest; ours takes file paths and prints digests.

- **`depcheck` / `license-checker` / `eslint-plugin-no-unsanitized`** →
  `tools/check`, a walker over `regexp` and `io/fs`. Fails the build on a
  `require` directive, a vendor directory, a CDN script or stylesheet, a remote
  `@import`, or a web font host. This is the premise of the project made
  executable rather than promised. A real linter has an AST, so it can tell a
  string literal from a comment; ours is regular expressions over raw bytes and
  will produce a false positive on prose that quotes forbidden markup.

## Server

- **`http-graceful-shutdown` / `stoppable`** → `http.Server.Shutdown` with
  `os/signal` and `context.WithTimeout`. Go put graceful shutdown in the
  standard library in 1.8, so the npm equivalents have no counterpart worth
  writing. The npm packages additionally track and close idle keep-alive
  sockets that a naive implementation leaks; `Shutdown` handles that itself.

- **`winston` / `pino` / `zerolog` / `logrus`** → `log` with a prefix and no
  flags. This binary logs startup, shutdown, and errors: perhaps six lines over
  a run. Structured JSON logging, levels, sampling, and rotation are real
  features that real services need, and we have none of those needs. If the
  ingest path ever needed per-request logging, `log/slog` is also stdlib and
  would be the next step, still with no dependency.

## Statistics

- **`hdrhistogram` / `t-digest` / `prom-client` histograms** → `internal/stats`,
  a fixed-bucket histogram with geometric boundaries for durations and linear
  boundaries for CLS. Around 200 lines over `math`. The published percentile is
  the geometric mean of its bucket, which bounds the relative error at
  sqrt(1.1)-1, about 4.9%. HdrHistogram is genuinely better: it auto-sizes,
  supports configurable significant digits, records a much wider dynamic range,
  and has a compact wire encoding for shipping histograms between processes. We
  need one fixed range and one process.

- **`simple-statistics` / `d3-array` quantile / `gonum/stat`** → a cumulative
  count walk in `Histogram.Quantile`. Exact percentiles require retaining and
  sorting every sample, which is O(n log n) and unbounded memory; this is O(1)
  per observation with flat memory. The libraries give exact answers with
  interpolation between order statistics, which ours deliberately does not do:
  interpolating between bucket bounds would imply precision the data lacks.

## Storage

- **`sqlite` / `postgres` + a driver (`mattn/go-sqlite3`, `lib/pq`,
  `better-sqlite3`)** → `internal/store`, a JSON Lines append log with an
  in-memory slice sorted by timestamp and a route index. No server to run, no
  driver to link, no CGO, no schema migration. A real database is better at
  almost everything else: concurrent writers, partial reads without loading the
  whole set into memory, crash-consistent transactions, indexes we did not
  hand-roll, and a query language. Ours holds every record in RAM, so it scales
  with the memory of one machine and no further.

- **`gorm` / `sqlc` / `prisma` / `knex`** → two functions,
  `Record.MarshalLine` and `UnmarshalLine`. The schema is one struct with five
  optional float fields; an ORM would be more code than the thing it maps. An
  ORM earns its place when the schema changes often and relations are deep, and
  neither is true here.

- **`json-iterator` / `easyjson` / `ffjson`** → `encoding/json` from the
  standard library for the on-disk record format. The faster encoders exist
  because reflection costs real time at high throughput; at one page view per
  visitor this is not close to the bottleneck. They are genuinely faster, by
  roughly two to four times on decode, and `easyjson` avoids reflection
  entirely by generating code. The ingest hot path gets a hand-written parser
  instead, for a different reason: untrusted input deserves a parser whose
  failure modes we chose ourselves.

- **`chokidar` / `fsnotify` / `tail`** → `os.ReadDir` plus `bufio.Scanner` on
  startup. The log is replayed once when the process starts and written only by
  this process, so nothing needs to watch the filesystem for changes. A watcher
  would be required if a second writer existed, and by design one does not.

## Ingest

- **`body-parser` / `express.json` / `encoding/json` for the hot path** → a
  hand-written scanner in `internal/ingest/parse.go`. This is the only place
  the program reads untrusted input, so the failure modes are worth owning: it
  accepts exactly one object shape, enforces its own size, route-length, and
  key-count limits, rejects before allocating, and cannot be pushed into
  unbounded work by a hostile body. `encoding/json` is better in every other
  respect: it handles arbitrary shapes, its number and string handling has been
  fuzzed by far more people than ours, and it supports streaming decode. Ours
  is fuzz-tested here (`FuzzParse`) precisely because that gap is real.

- **`uuid` / `nanoid` for a per-page-view identifier** → eight characters of
  `Math.random().toString(36)` plus the timestamp in base 36, one line in
  `beacon.full.src.js`. It exists so a payload delivered twice is stored once,
  never leaves the page view, and is not persisted in the browser or on disk.
  `Math.random` is not cryptographically strong and does not need to be: a
  collision costs one dropped duplicate, and `crypto.randomUUID` would have been
  36 characters and unavailable on an insecure origin. Where the packages are
  better: they are collision-resistant at a scale this is not, and
  `crypto.randomUUID` is genuinely random where this is not.

- **`uuid` / `nanoid` / `cuid` for visitor identifiers** → `crypto/sha256` over
  the address, user agent, and UTC date, truncated to 8 hex characters. A
  generated identifier would need to be stored somewhere to be stable, and
  storing it is what a cookie is. Deriving it means there is nothing to store,
  nothing to sync, and no identifier that outlives the day. A real ID library
  gives collision-resistant globally unique values; ours collides deliberately,
  because collapsing similar visitors is the privacy property we want.

- **`cors`** → nothing at all. `navigator.sendBeacon` posts `text/plain`, which
  is a CORS-simple request: the browser sends it cross-origin with no preflight
  and never needs to read the response. Adding CORS headers would be cargo
  cult. The `cors` package is genuinely necessary the moment an endpoint must
  return a readable response cross-origin, which this one deliberately does not.

- **`express-rate-limit` / `rate-limiter-flexible` / `golang.org/x/time/rate`**
  → `internal/ingest.Limiter`, about 120 lines: a token bucket per client
  address, refilled lazily.

  `golang.org/x/time/rate` is the obvious answer in Go and is explicitly banned
  by the event rules, since `golang.org/x` is not the standard library. It is
  also only half of what is needed here: it limits one bucket, and the thing to
  bound is a map of buckets keyed by address, which is itself an unbounded
  allocation if nobody thinks about it.

  So the buckets are lazily refilled rather than driven by a timer, which means
  an idle client costs one map entry and no goroutine; the table is capped at
  8,192 sources and evicts the least recently seen; and a sweep drops anything
  idle for ten minutes. The check runs before the request body is read, so a
  client over its limit costs a map lookup rather than a parse.

  Where theirs is better: `golang.org/x/time/rate` has a proper `Wait` with
  context cancellation, reservation and cancellation semantics, and arithmetic
  that has been reviewed far more carefully than this. `rate-limiter-flexible`
  can share state across processes through Redis, which is what a real
  multi-instance deployment needs and this cannot do at all.

  The limit is per client address with forwarded headers ignored, so behind a
  reverse proxy every visitor shares one bucket. That is a real limitation and
  the answer is `-rate -1` plus a limit at the proxy.

- **`express` + `serve-static`** → `internal/httpx.FileServer` over
  `net/http` and `io/fs`. Routing is `http.ServeMux`, which since Go 1.22
  handles method and wildcard patterns. Express gives middleware composition, a
  router with parameter extraction, view engines, and an ecosystem; this serves
  a fixed set of embedded files and answers three JSON endpoints, and needs
  none of that.

- **`compression` / `gorilla/handlers.CompressHandler`** → `compress/gzip`,
  applied once at startup rather than per request. Because the asset set is
  embedded and immutable, every file is compressed when the server is
  constructed and the response is a slice copy. The middleware versions
  compress per request, which is the right design for dynamic responses and the
  wrong one here. They also support Brotli and Zstd, which we do not: a browser
  offering `br` gets the uncompressed body.

- **`etag` / `fresh`** → `crypto/sha256` over the file contents, truncated to
  128 bits and base64-encoded. Hashing the content rather than stat-ing the
  file is what makes the tag survive a rebuild: an embedded asset has no
  meaningful modification time, and two builds of identical bytes should share
  a cache entry. The `etag` package additionally handles `If-Match` and range
  requests; we handle `If-None-Match` only, because that is the one a browser
  sends for a static asset.

- **`mime-types` / the standard `mime` package** → a fixed extension table in
  `internal/httpx/mime.go`. `mime.TypeByExtension` consults the host's MIME
  database, which on Windows means the registry, so the same binary can serve
  JavaScript as `text/plain` on one machine and correctly on another. A
  hardcoded table of fourteen extensions is wrong the same way everywhere,
  which is the property that matters. Unknown extensions get
  `application/octet-stream` rather than being sniffed.

- **`helmet` / `serve-static` traversal guards** → `path.Clean` plus a map
  lookup. Assets are looked up by key in a map built at startup, never opened
  from the filesystem by request path, so directory traversal has nothing to
  traverse to. This is a stronger guarantee than a path-prefix check, and it is
  a property of the design rather than a check that could be forgotten.

## Beacon

- **`web-vitals` (Google)** → `internal/beacon/beacon.src.js`, hand-written over
  `PerformanceObserver`, 942 bytes raw and 571 gzipped against 7,226 and 2,601
  for `web-vitals` 4.2.4, both measured by `tools/compare` with the same
  compressor. 7.7x smaller raw, 4.6x gzipped, and ours also carries the
  transport that `web-vitals` leaves to the caller.

  Where theirs is better, specifically:

  - **INP is real.** `web-vitals` tracks full interaction latency across every
    event in an interaction and reports a high percentile. This one reports the
    maximum duration of a single event over 16ms. Directionally right,
    pessimistic in the tail.
  - **Back-forward cache.** A page restored from bfcache is a new page view
    with new metrics. `web-vitals` handles the restore and re-reports; this one
    does not, so a back-navigation is silently missed.
  - **Soft navigations.** Single-page-app route changes produce no new
    metrics here at all.
  - **Prerendering** and `activationStart` correction: not handled.
  - **A page hidden before it painted.** `web-vitals` discards paint timings
    reported after the page first became hidden. This one does not, so a page
    opened in a background tab contributes an LCP nobody saw.
  - **Browser quirks.** Years of accumulated workarounds for older Safari and
    for LCP edge cases that this one simply does not have.

  It is correct for a normal page load on current Chrome, Firefox, and Safari,
  and that is the claim being made.

- **`web-vitals` (Google), the parity build** →
  `internal/beacon/beacon.full.src.js`, served at `/b-full.js`. 2,656 bytes raw
  and 1,415 gzipped, against 7,226 and 2,601 for `web-vitals` 4.2.4 and 12,505
  and 4,172 for its attribution build. It closes five of the six gaps listed
  above: real INP grouped by `interactionId` with the specification's
  discard-one-per-fifty percentile rule, back-forward cache restores, soft
  navigations, `activationStart` correction, and the hidden-before-paint
  discard.

  This is a second beacon rather than a replacement because the sub-1KB claim is
  about the script a site puts on every page by default. Raising that budget to
  fit these features would have made the claim untrue rather than optional, so
  the two are served side by side under separate budgets.

  Where theirs is still better:

  - **Verification.** `web-vitals` has years of field use across every browser.
    The parity build's server half is tested end to end, but no browser has
    exercised its bfcache, soft-navigation, prerender, or interaction-grouping
    paths. They are written against the specifications and reviewed, not
    demonstrated. This is the honest gap and it is the largest one.
  - **Soft navigation detection.** `web-vitals` can use the `soft-navigation`
    performance entry. This wraps `history.pushState` and `history.replaceState`
    instead, so a route change made without the History API is missed.
  - **bfcache paint timings.** `web-vitals` re-reports properly; this
    approximates FCP and LCP as the delay from the restore to the next frame.
  - **Interaction retention.** Both keep the ten longest interactions, but
    `web-vitals` handles an interaction whose latency grows after it has been
    evicted from that set. This one does not, and can therefore under-report.
  - **Browser quirks**, as above.

- **`web-vitals/attribution`** → about 30 lines across `beacon.full.src.js` and
  `internal/dash/report.go`: a selector built from the tag plus an id or first
  class, plus a count of how often each selector was named in a page view rated
  poor. 2,656 bytes against 12,505 for their attribution build.

  Theirs is substantially better and it is not close. `web-vitals` reports the
  subpart timing breakdown of an interaction, input delay against processing
  against presentation, long-animation-frame data, the LCP resource URL with its
  load phases, and the full shift source list. This reports which element, and
  nothing about why. It is also not a unique path, so identical sibling elements
  are counted as one, which is the right behaviour for an aggregate and the
  wrong one for a page built from a single repeated component.

- **`lru-cache` / `quick-lru` / `hashicorp/golang-lru`** → `recentIDs` in
  `internal/ingest/handler.go`, a `map` alongside a fixed-size ring of keys
  under one mutex, about 30 lines. It drops a beacon payload whose page-view
  identifier has already been stored, which is what makes the `sendBeacon` and
  keepalive-fetch race harmless.

  Deliberately not an LRU: every key here is looked up at most twice, so recency
  of *insertion* is the only ordering that means anything, and an LRU would keep
  a popular key alive forever. Where a real cache library is better: eviction is
  a plain ring rather than anything adaptive, there is no TTL, and the whole set
  is discarded on restart. All three are fine for a window that only has to
  outlast a burst of a few seconds, and would not be fine for a general cache.

- **`history` (npm) / a router's navigation events** → wrapping
  `history.pushState` and `history.replaceState` and listening for `popstate`,
  eight lines in `beacon.full.src.js`. It is how a client-side route change gets
  noticed without the beacon knowing which router the site uses. The wrapper
  calls through first and reports second, so a router that throws fails exactly
  as it would have unwrapped. Where the packages are better: they own the
  navigation rather than observing it, so they see a route change made without
  the History API, which this misses entirely.

- **`terser` / `esbuild` / `uglify-js`** → hand minification. `beacon.src.js`
  is the commented source a reviewer reads; `beacon.min.js` is written by hand
  from it. The same arrangement, and the same cost, applies to the pair under
  `beacon.full.*`. A minifier is a build-time dependency, and this project has no build
  step at all. The honest cost: the two files are kept in sync by a human, so
  they can drift. `beacon_test.go` asserts every metric key, entry type, and
  endpoint appears in both, and `TestMinifiedIsActuallyMinified` catches the
  worst mistake, which is shipping the readable file under the minified name. A
  real minifier would make drift structurally impossible.

- **`webpack` / `rollup` / `vite`** → no bundler, because there is nothing to
  bundle. The beacon is one IIFE in one file with no imports. Bundlers earn
  their place with module graphs, tree shaking, and code splitting; a
  single-file script has none of those problems.

## Dashboard

- **React / Vue / Svelte** → about 400 lines of vanilla JavaScript in
  `dash.js`. The dashboard renders four API responses into a scorecard, a
  chart, two tables, and a counter row, and re-renders the whole thing on each
  refresh. There is no shared mutable state and no partial update, so the thing
  a framework is for does not arise. A framework earns its place when state is
  complex and updates are fine-grained; here the entire page is a pure function
  of one fetch.

- **Chart.js / D3 / Recharts / ApexCharts** → about 120 lines building inline
  SVG with `createElementNS`. A `polyline` for the series, `line` for axes,
  grid, and thresholds, `circle` for points, `text` for ticks. Gaps in the data
  break the polyline instead of interpolating, which is a deliberate difference
  from most charting defaults: an interpolated line across a window with no
  page views would be inventing data. What the libraries do that this does not:
  zoom and pan, animated transitions, automatic tick selection, stacked and log
  scales, and legends. D3's axis and scale modules alone are more capable than
  everything here.

- **`webpack` / `vite` / `parcel` / any bundler or transpiler** → no build
  step at all. Three files are served exactly as they are written, embedded in
  the binary with `//go:embed`. The cost is real: no minification, no module
  system, no tree shaking, and the source is what ships. At this size that is a
  fair trade, and it means the file a judge reads is the file the browser runs.

- **`normalize.css` / Tailwind / Bootstrap / any CSS framework** → about 470
  lines of hand-written CSS driven by custom properties. Tailwind needs a build
  step to be anything other than enormous, and a component framework would ship
  a grid system, a modal, and a typography scale to render five cards and two
  tables. The palette is three status colours from the Core Web Vitals banding
  plus one identity hue per metric; gradients, tinted surfaces, and the pill
  badges are all `color-mix` and `linear-gradient`, so the whole visual system
  costs 3.8KB gzipped and zero requests. What a framework genuinely brings that
  this does not: a tested cross-browser reset, documented components, and
  someone else maintaining the dark mode.

- **`date-fns` / `moment` / `dayjs`** → `Date` and `Intl` methods already in
  the browser, plus `time` on the server. The dashboard formats a clock time
  and a byte count; that is two small functions. These libraries are genuinely
  better at timezone arithmetic, relative phrasing, and locale-aware formats,
  none of which this needs.

- **`axios` / `superagent`** → `fetch`, which every browser that supports
  `PerformanceObserver` also supports. Axios adds interceptors, automatic JSON
  handling, request cancellation, and consistent error shapes; the four calls
  here share one eight-line wrapper instead.

- **`clipboard.js`** → `navigator.clipboard.writeText`, with the copied text
  written into a `<textarea>` first so a refusal has somewhere to fall back to.
  `clipboard.js` exists because `document.execCommand('copy')` needed a
  selected node and a user gesture on browsers we no longer target, and it
  still handles those, plus copy-from-attribute and cut. The async clipboard
  API covers what this needs in one call; it is unavailable outside a secure
  context, and localhost counts as one.

- **`file-saver`** → `URL.createObjectURL` over a `Blob`, a synthesised `<a
  download>`, and a `revokeObjectURL` on a timer. `file-saver` is better on the
  cases it was written for: Safari's missing `download` support, IE's
  `msSaveBlob`, and files large enough to need a filesystem writer. A report
  measured in kilobytes, saved from a dashboard the developer is running
  locally, does not reach any of them.

- **`socket.io` / `ws` / `sse.js`** → 60 lines of `net/http` writing
  `text/event-stream`, and the browser's own `EventSource`. Live updates are
  one-directional and about 90 bytes per page view, so a WebSocket would mean
  hand-writing a handshake, frame headers, and client-to-server masking for
  traffic that never flows that way. `socket.io` is better the moment you need
  rooms, acknowledgements, binary frames, or a fallback for a network that
  blocks streaming; it also reconnects with backoff, where we rely on
  `EventSource`'s fixed retry. Ours drops notifications for a subscriber that
  has fallen 8 behind rather than buffering, which is right for a signal that
  says "re-read" and wrong for a chat message.

- **`cobra` / `urfave/cli`** → `flag.NewFlagSet` per subcommand and a switch on
  `os.Args[1]`. Two commands, `serve` and `report`, do not need a command tree,
  shell completion, or nested help. `cobra` earns its place at ten commands and
  a plugin system; here it would be 40,000 lines to route one string.

- **`tablewriter` / `lipgloss`** → `fmt.Fprintf` with fixed column widths in
  `report.go`. The terminal report has one table shape and one alignment rule.
  `tablewriter` handles what we do not: terminal width detection, wrapping,
  multi-line cells, and East Asian character widths, where our fixed columns
  will misalign on a route with wide glyphs.

- **`cron` / `robfig/cron` / an external `logrotate`** → a `time.Ticker` on an
  hour and a directory walk that removes whole day files. Retention here is one
  rule, day-granular, so a cron expression parser would be the only thing in the
  binary that needed one. `logrotate` is better at everything about rotation
  that we do not do: compression, copy-truncate for open files, size triggers,
  and post-rotate hooks.

## Demo site

- **A templating engine (`handlebars`, `ejs`, `html/template`)** → a small
  Python generator run once at authoring time, whose output is committed. The
  four demo pages share a navigation and a footer, which is the entire
  templating requirement. `html/template` is in the standard library and would
  have been free of dependency cost, but rendering at request time would mean
  the file a judge reads is not the file the browser receives. Committing the
  generated HTML keeps those identical. The generator is not part of the build
  and the binary does not contain it.

- **A placeholder image service or a committed binary asset** → an inline SVG
  of 2,600 generated shapes. The heavy demo page needs to be genuinely
  expensive to paint, and pointing at `picsum.photos` or `placehold.co` would
  put a third-party request in the demo of a tool that argues against
  third-party requests. Committing a large PNG would work too, but the SVG
  costs real parse and raster time rather than only download time, which is a
  better demonstration of what LCP measures.

## Reproducible build

- **`goreleaser` / `ko` / a build script that stamps a version** → four flags on
  `go build`, and deliberately no stamping at all: `-trimpath`,
  `-buildvcs=false`, `-ldflags="-s -w -buildid="`. The interesting one is
  `-buildvcs=false`. Since Go 1.18 the toolchain writes `vcs.revision`,
  `vcs.time`, and a module pseudo-version into any binary built inside a git
  repository, so a build that looks reproducible produces a different hash on
  every commit. We shipped that mistake first and the README claimed a property
  the binary did not have; `go version -m` now shows no `vcs.*` entries.
  `goreleaser` is genuinely better at everything a release actually needs:
  cross-compilation matrices, checksums, signing, changelogs, and publishing. We
  need one binary and one property, and that property is now verified by CI on
  every push rather than asserted in a README.
