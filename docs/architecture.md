# Architecture

The structure of the system, the path a measurement takes through it, and the
constraints that follow from the design.

Related documents: [`beacon.md`](beacon.md) for the client script,
[`api.md`](api.md) for the HTTP surface, [`storage.md`](storage.md) for the
on-disk format and percentile arithmetic, and
[`development.md`](development.md) for the build and test process.

---

## 1. System overview

```mermaid
flowchart LR
    subgraph browser["Browser"]
        page["Instrumented page"]
        beacon["beacon.min.js<br/>942 bytes"]
        dash["Dashboard<br/>vanilla JS + inline SVG"]
        page --> beacon
    end

    subgraph binary["vitals binary (single process)"]
        httpx["src/internal/httpx<br/>static assets"]
        ingest["src/internal/ingest<br/>parse, validate, derive session"]
        store["src/internal/store<br/>append log + in-memory index"]
        stats["src/internal/stats<br/>histograms, percentiles, banding"]
        api["src/internal/dash<br/>JSON API"]
    end

    disk[("data/YYYY-MM-DD.jsonl")]

    beacon -- "POST /v1/collect" --> ingest
    ingest --> store
    store --> disk
    disk -. "replay at startup" .-> store
    api --> store
    api --> stats
    dash -- "GET /api/*" --> api
    httpx -- "serves" --> beacon
    httpx -- "serves" --> dash
```

A single process serves the dashboard, the demo site, the beacon, the collection
endpoint, and the JSON API on one port. This is what allows the tool to run as
one command with no configuration file and no external services.

## 2. Package layout

| Package | Responsibility |
|---|---|
| `src/cmd/vitals` | Flags, graceful shutdown, logging |
| `src/server` | The module's only exported package: opens the store and builds the route table |
| `src/internal/httpx` | Static file serving: MIME, ETag, gzip, cache policy |
| `src/internal/beacon` | Client script, embedded and size-checked |
| `src/internal/ingest` | Payload parsing, validation, session derivation, counters |
| `src/internal/store` | Append log, replay, in-memory index, range queries |
| `src/internal/stats` | Histograms, approximate percentiles, banding |
| `src/internal/dash` | JSON API handlers and dashboard assets |
| `src/internal/demo` | Bundled demo site |
| `src/tools/` | Build-time utilities: dependency check, hashing, size reporting |
| `tests/` | Black-box tests over the served HTTP surface; see `tests/README.md` |

`src/server` exists so that a test, or an embedder, gets the routing the binary
serves rather than a second copy assembled for the occasion. Everything it
wires together stays under `src/internal`, which the compiler keeps private to
this module.

Dependencies run in one direction only:

```mermaid
flowchart TD
    cmd["src/cmd/vitals"]
    srv["src/server"]
    dash["src/internal/dash"]
    demo["src/internal/demo"]
    beacon["src/internal/beacon"]
    ingest["src/internal/ingest"]
    store["src/internal/store"]
    stats["src/internal/stats"]
    httpx["src/internal/httpx"]

    cmd --> srv
    srv --> dash
    srv --> demo
    srv --> beacon
    srv --> ingest
    srv --> store
    dash --> store
    dash --> stats
    dash --> ingest
    dash --> beacon
    dash --> httpx
    demo --> httpx
    beacon --> httpx
    ingest --> store
    ingest --> stats
    store --> stats
```

`src/internal/stats` and `src/internal/httpx` are leaves and depend on nothing within
the project. There are no cycles.

Assets under `src/internal/beacon`, `src/internal/dash/assets`, and
`src/internal/demo/site` are embedded with `//go:embed`, making the binary
standalone. They reside inside the packages that embed them because `//go:embed`
cannot reference paths outside its own package directory.

## 3. Collection path

```mermaid
sequenceDiagram
    participant P as Page
    participant B as beacon.min.js
    participant H as Ingest handler
    participant S as Store
    participant D as Disk

    P->>B: load /b.js (defer)
    B->>B: register PerformanceObserver<br/>navigation, paint, LCP, layout-shift, event
    Note over B: buffered:true replays<br/>entries from before load
    P-->>B: visibilitychange to hidden
    B->>B: serialise one JSON object
    B->>H: POST /v1/collect (text/plain, sendBeacon)
    H->>H: cap body at 4096 bytes
    H->>H: parse and validate
    H->>H: derive session id
    H->>S: Append(record)
    S->>S: buffer write, update index
    H-->>B: 204 No Content
    Note over S,D: flush every 200 records<br/>or 2 seconds
    S->>D: append JSON line
```

The response is returned before the record reaches disk. Ingest never waits on
I/O.

## 4. Query path

```mermaid
sequenceDiagram
    participant U as Dashboard
    participant A as API handler
    participant S as Store
    participant St as stats.Histogram

    U->>A: GET /api/summary?from=24h
    A->>A: parse and bound the time range
    A->>S: Each(range, fn)
    S->>S: binary search sorted index
    loop each record in range
        S-->>A: record
        A->>St: Add(value)
    end
    A->>St: Quantile(0.75)
    St-->>A: approximate p75
    A->>A: band against CWV thresholds
    A-->>U: JSON
```

## 5. Validation pipeline

Every rejection is counted rather than reported to the client.

```mermaid
flowchart TD
    start["POST /v1/collect"] --> method{"Method is POST?"}
    method -- no --> m405["405 Method Not Allowed"]
    method -- yes --> size{"Body ≤ 4096 bytes?"}
    size -- no --> ctooLarge["counter: tooLarge"]
    size -- yes --> parse{"Parses as one<br/>JSON object?"}
    parse -- no --> cmal["counter: malformed"]
    parse -- yes --> route{"Route present<br/>after sanitising?"}
    route -- no --> cmal
    route -- yes --> metrics{"At least one<br/>known metric?"}
    metrics -- no --> cmal
    metrics -- yes --> append["Append to store"]
    append -- error --> cerr["counter: storeErrors"]
    append -- ok --> cok["counter: accepted"]

    ctooLarge --> r204["204 No Content"]
    cmal --> r204
    cerr --> r204
    cok --> r204
```

Returning `204` in every case is deliberate. A beacon cannot act on an error and
will not retry usefully, and returning `4xx` to `sendBeacon` fills the visitor's
console while still losing the sample. Counters are exposed through
`/api/summary` and displayed on the dashboard, so rejections remain visible to
the operator.

## 6. Design decisions

**The server clock is authoritative.** The client timestamp is parsed and
range-checked but never used for storage. A visitor with an incorrect clock
would otherwise distribute records arbitrarily across the timeline.

**No CORS headers are set.** `sendBeacon` transmits `text/plain`, which
constitutes a CORS-simple request: the browser sends it cross-origin without a
preflight and never reads the response.

**Sessions are derived rather than stored.** The identifier is
`sha256(ip + user-agent + UTC date)` truncated to eight hexadecimal characters.
No cookie is set, no persistent identifier is issued, the IP address is never
written to disk, and the value rotates at midnight UTC, so a visitor cannot be
correlated across days.

**Device class is derived from viewport width.** User-agent parsing is
unreliable, is being progressively restricted by browsers, and would constitute
a fingerprinting surface. Breakpoints are 767px and 1023px.

## 7. Concurrency

The store is guarded by a `sync.RWMutex`. Appends hold the write lock only long
enough to buffer the record and update the index, never for the duration of a
disk write. A background goroutine flushes the buffer every two seconds.

Reads hold the read lock for the duration of iteration, so a callback passed to
`Each` must not re-enter the store.

Ingest counters use `atomic.Uint64`.

The suite runs under the race detector in CI on every push. It has not been run
under the race detector on Windows, where the detector requires a C toolchain.

## 8. Constraints of this design

The following are properties of the architecture rather than defects.

| Constraint | Consequence |
|---|---|
| Entire record set held in memory | Memory grows with total records; capacity is bounded by one machine |
| Single writer, no locking | Two processes sharing a data directory will corrupt the log |
| No retention policy | `data/` grows until pruned manually |
| No authentication | Dashboard and API are open to anyone who can reach the port |
| No rate limiting on collection | A client can inflate the numbers |
| Route is the only secondary index | Device and session queries scan the range |

At the intended scale, a single site producing thousands of page views per day,
an append log with a sorted in-memory index is the appropriate design rather
than a compromise. It would not withstand deployment against a large site, and
is not intended to.
