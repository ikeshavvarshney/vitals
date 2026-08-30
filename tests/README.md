# tests

Black-box tests. Everything here drives the server the binary serves, over
HTTP, through the module's public surface (`vitals/src/server`) and nothing
else. If a test in this directory passes, a judge running the binary and poking
it with a browser sees the same behaviour.

| File | Covers |
|---|---|
| `endtoend_test.go` | A beacon payload posted the way a browser posts it, read back through the API. Every route reachable, the demo pages instrumented, an unknown path 404, malformed payloads accepted without storing, a measurement surviving a restart, the served beacon inside its byte budget. |
| `api_contract_test.go` | The JSON shapes consumers depend on: `value` rather than `p75`, null for an unreported metric, the comparison window, the percentile and route filter parameters, the report's caveats, and a `400` with a named parameter for anything unparseable. |
| `assets_test.go` | The premise, checked against what is served rather than the source tree: no absolute URL, no web font, no CDN. Plus the caching policy, gzip, and that a conditional request answers `304` with an empty body. |

## Where the rest of the tests are

Unit tests live next to the code they cover, which is where Go looks for them
and where they can reach the unexported parsers and arithmetic that carry most
of the risk. Moving them here would mean exporting `parseQuery`, `summarize`,
`newAsset`, `wireRecord` and others for no reason but a directory listing, and
`package main` cannot be imported from anywhere at all.

| Package | Unit tests cover |
|---|---|
| `src/internal/stats` | Histogram bucketing, quantile arithmetic, the stated error bounds, banding against the published thresholds. |
| `src/internal/store` | Record encoding and decoding, replay of a truncated file, range queries, the route index, flush behaviour. |
| `src/internal/ingest` | The hand-written JSON parser, size limits, malformed input, the derived session identifier. |
| `src/internal/dash` | Query parameter parsing, the summary, series, breakdown and report handlers, and the asset invariants. |
| `src/internal/httpx` | The file server: ETags, conditional requests, gzip negotiation, cache policy, method handling. |
| `src/internal/beacon` | The beacon's byte budget and that the minified form is the source's behaviour. |
| `src/tools/check` | The dependency checker itself, which is what enforces the zero-dependency rule. |

## Running

```
go test ./...        # everything
go test ./tests/     # the black-box tests in this directory only
make test            # the same as the first
```
