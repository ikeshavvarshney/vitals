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

- **`express-rate-limit` / `helmet`** → not used, and this is a stated gap
  rather than a substitution. The collection endpoint has no rate limiting: a
  determined client can post as fast as the network allows and inflate the
  numbers. The body cap, the metric plausibility bounds, and the 204-always
  response limit the damage, but they are not a rate limiter. For a
  self-hosted tool on a small site this is an accepted risk, not a solved
  problem.

## HTTP serving

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
  `PerformanceObserver`, 942 bytes raw and 571 gzipped. This is the headline
  substitution: the tool that measures page weight adds 942 bytes to a page
  instead of several kilobytes of someone else's JavaScript from someone else's
  CDN.

  Where theirs is better, specifically:

  - **INP is real.** `web-vitals` tracks full interaction latency across every
    event in an interaction and reports a high percentile. Ours reports the
    maximum duration of a single event over 16ms. Directionally right,
    pessimistic in the tail.
  - **Back-forward cache.** A page restored from bfcache is a new page view
    with new metrics. `web-vitals` handles the restore and re-reports; ours
    does not, so a back-navigation is silently missed.
  - **Soft navigations.** Single-page-app route changes produce no new
    metrics here at all.
  - **Prerendering** and `activationStart` correction: not handled.
  - **Browser quirks.** Years of accumulated workarounds for older Safari and
    for LCP edge cases that ours simply does not have.

  Ours is correct for a normal page load on current Chrome, Firefox, and
  Safari, and that is the claim being made.

- **`terser` / `esbuild` / `uglify-js`** → hand minification. `beacon.src.js`
  is the commented source a reviewer reads; `beacon.min.js` is written by hand
  from it. A minifier is a build-time dependency, and this project has no build
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

- **React / Vue / Svelte** → about 380 lines of vanilla JavaScript in
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

- **`normalize.css` / Tailwind / any CSS framework** → about 340 lines of hand
  written CSS with custom properties for the palette. Tailwind would need a
  build step to be anything other than enormous. The status colours are the
  only saturated values on the page, and they come from the Core Web Vitals
  banding rather than from a design system.

- **`date-fns` / `moment` / `dayjs`** → `Date` and `Intl` methods already in
  the browser, plus `time` on the server. The dashboard formats a clock time
  and a byte count; that is two small functions. These libraries are genuinely
  better at timezone arithmetic, relative phrasing, and locale-aware formats,
  none of which this needs.

- **`axios` / `superagent`** → `fetch`, which every browser that supports
  `PerformanceObserver` also supports. Axios adds interceptors, automatic JSON
  handling, request cancellation, and consistent error shapes; the four calls
  here share one eight-line wrapper instead.

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
