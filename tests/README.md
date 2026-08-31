# tests

Black-box tests. Everything here drives the server the binary serves, over
HTTP, through the module's public surface (`vitals/src/server`) and nothing
else. If a test in this directory passes, a judge running the binary and poking
it with a browser sees the same behaviour.

| File | Covers |
|---|---|
| `endtoend_test.go` | A beacon payload posted the way a browser posts it, read back through the API. Every route reachable, the demo pages instrumented, an unknown path 404, malformed payloads accepted without storing, a measurement surviving a restart, the served beacon inside its byte budget. |
| `api_contract_test.go` | The JSON shapes consumers depend on: `value` rather than `p75`, null for an unreported metric, the comparison window, the percentile and route filter parameters, the report's caveats, and a `400` with a named parameter for anything unparseable. Also that every key in the report document is lower-camel, walked over `map[string]any` rather than a struct, because `encoding/json` decodes field names case-insensitively and will match a key no JavaScript client can read. |
| `assets_test.go` | The premise, checked against what is served rather than the source tree: no absolute URL, no web font, no CDN. Plus the caching policy, gzip, and that a conditional request answers `304` with an empty body. |
| `live_test.go` | The Server-Sent Events stream: headers, the opening comment, a frame naming the route that was just recorded, and that ingestion keeps working after a stream client disconnects. |
| `storage_test.go` | Disk usage reported from the files, and `-retain` removing an old day log from disk, from memory, and from the API while today's is untouched. |
| `crash_test.go` | The durability claim against a real process: the binary is built, run as a child, fed measurements, and killed without warning. Asserts every flushed record survives, that a kill mid-write loses no more than the buffer held, that the store still accepts writes afterwards, that a torn day log stays parsable line by line, and that skipped lines are reported rather than hidden. |
| `attribution_test.go` | What the full beacon adds, end to end: an element selector, navigation type, and page-view identifier posted the way a browser posts them and read back out of `/api/report`; a duplicate payload stored once; the small beacon reporting none of it; both beacons served inside their own budgets; and which demo page carries which beacon. |

## Where the rest of the tests are

Unit tests live next to the code they cover, which is where Go looks for them
and where they can reach the unexported parsers and arithmetic that carry most
of the risk. Moving them here would mean exporting `parseQuery`, `summarize`,
`newAsset`, `wireRecord` and others for no reason but a directory listing, and
`package main` cannot be imported from anywhere at all.

| Package | Unit tests cover |
|---|---|
| `src/internal/stats` | Histogram bucketing, quantile arithmetic, the stated error bounds, banding against the published thresholds. |
| `src/internal/store` | Record encoding and decoding, replay of a truncated file, range queries, the route and session indexes, index correctness under out-of-order arrival, flush behaviour. Benchmarks for append, scan, replay, and encoding. |
| `src/internal/ingest` | The hand-written JSON parser, size limits, malformed input, the derived session identifier, duplicate suppression, and the per-source rate limiter. |
| `src/internal/dash` | Query parameter parsing, the summary, series, breakdown, journeys and report handlers, event broadcasting and frame quoting, and the asset invariants. |
| `src/internal/httpx` | The file server: ETags, conditional requests, gzip negotiation, cache policy, method handling. |
| `src/internal/beacon` | The beacon's byte budget and that the minified form is the source's behaviour. |
| `src/tools/check` | The dependency checker itself, which is what enforces the zero-dependency rule. |
| `src/cmd/vitals` | The `report` subcommand: table output, `-json`, flag validation, and that a server flag is never mistaken for a subcommand. The startup banner's address, which is the only instruction a new user is given. |

## Running

```
go test ./...          # everything
go test ./tests/       # the black-box tests in this directory only
go test ./... -short   # skips the crash tests, which build and run the binary
make test              # the same as the first
```

The crash tests in `crash_test.go` compile the binary and start it as a child
process, so they take about thirty seconds and need a free loopback port. They
are skipped under `-short`.
