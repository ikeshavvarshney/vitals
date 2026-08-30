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
