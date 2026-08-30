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
