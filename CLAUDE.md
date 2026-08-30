# CLAUDE.md

Working rules for this repository. Read `PRD.md` for scope, `DESIGN.md` for
architecture.

---

## The one rule

**This project ships with zero third-party runtime dependencies.** `go.mod` has
no `require` block. Nothing is fetched at runtime. This is not a preference.
It is the entire premise of the project and it is verified by judges in five
seconds.

## Hard prohibitions

Never do any of the following, even when it is the obvious fix:

- `go get` anything. If you find yourself wanting a package, that want is the
  work: implement it and log it in `STDLIB.md`.
- Add a `require` block to `go.mod`. `golang.org/x/...` is **not** stdlib and is
  explicitly banned by the event rules.
- Add `<script src="http...">`, `<link href="http...">`, or any CDN reference to
  any HTML file.
- Load a web font from anywhere. System font stack only.
- Copy a library's source into this repo. That is vendoring; it is a dependency
  with extra steps.
- Shell out to a tool that is not part of the Go toolchain. Calling out to `git`,
  `jq`, or `curl` at runtime is a hidden dependency.
- Add a test framework. Go's `testing` package is stdlib and is sufficient.

If a task appears to be impossible without one of the above, stop and say so
rather than working around it quietly.

## Before you finish any task

Run `make check`. It fails the build on a `require` block, a CDN reference, or a
web font. If it fails, fix the code, never the check.

## STDLIB.md is written continuously

Every time you decide not to use a package, append the entry immediately. Format:

```
- **`web-vitals`** → hand-written `PerformanceObserver` collection in
  `beacon.src.js`. 1.4KB vs 45KB. Theirs handles bfcache restoration and more
  browser quirks; ours does not.
```

Each entry names the package, the stdlib or hand-written replacement, and
(this part matters) where the replaced package is genuinely better. Fifteen
honest entries are worth more than thirty padded ones. Never write an entry for
a package we would not actually have used.

Do not batch these up to write at the end. They will be vague and they will be
worth fewer points.

## Code standards

- Idiomatic Go, as a senior reviewer would expect. This is 25% of the score.
- Errors are wrapped with context and handled. Never `_ = err`.
- No `panic` in request paths.
- Exported identifiers have doc comments; packages have a package comment.
- Table-driven tests for every parser and every piece of arithmetic.
- Prefer standard library idioms over cleverness. Fighting the stdlib is
  explicitly penalised by the rubric; using it well is the point.

## Frontend standards

- Vanilla HTML, CSS, JS. No framework, no bundler, no transpiler, no build step.
- Charts are inline SVG generated in JS. No canvas library, no chart library.
- Accessibility floor: keyboard reachable, visible focus, `prefers-reduced-motion`
  respected, responsive to 360px.
- The beacon has a hard 1024-byte raw budget. `make beacon` enforces it.

## Honesty requirements

The rubric explicitly rewards disclosed limitations and penalises hidden ones.
When you implement something approximate, document it in the same commit:

- Percentiles are bucketed approximations, not exact. State the error bound.
- INP is approximated. Say what the approximation is.
- Buffered writes lose up to 2 seconds on crash. Say so.
- If our version is slower or weaker than the package it replaces, say that too.

Never write a comment or README line claiming a property the code does not have.

## Working style

- Write the test before the implementation for anything that parses input.
- Prefer finishing a P0 item to starting a P1 item. Check `PRD.md` cut lines
  before adding scope.
- Ask before adding a feature that is not in `PRD.md`.

## Commit cadence

Commit at the end of each substantive piece of work, not in one batch at the end
of the project and not after every keystroke. Judges read the history: three
enormous commits look like generated code, and forty one-line commits look like
noise. Working at the right granularity lands somewhere around **25-30 commits
by completion**. That is an expected outcome, not a quota. Never split work
apart or invent commits to reach it.

A commit is warranted when a unit of work is genuinely finished:

- A package does something it could not do before, with its tests.
- A subsystem is refactored and still passes.
- A real bug is fixed.
- A document is written or substantially revised.
- A build target or check is added and works.

Do not commit for:

- A one-line edit, a typo, a reworded sentence, or a renamed variable.
- Work in progress that does not stand on its own.
- Reaching a commit count.

Fold small changes into the next substantive commit instead. If a trivial fix is
genuinely urgent on its own, that is the exception, not the pattern.

Rules for each commit:

- One logical change per commit. Do not mix a refactor with a feature.
- The tree builds and `make check` passes at every commit. Never commit a
  knowingly broken tree.
- Subject line in the imperative mood, under 72 characters, no trailing period.
  Add a body only when the reasoning is not obvious from the diff.
- No co-author trailers and no tool attribution in commit messages.
- Never squash the history to tidy it up. The granularity is the point.
- Ask before pushing. Committing locally and pushing are separate decisions.

## Time constraint

The code freeze is **August 31, 2026 at 18:00 UTC**. The last three hours are
reserved for README, `STDLIB.md`, `deps-proof.txt`, and the demo video, not for
code. If a feature is not working by then, it is cut and its absence is
documented rather than half-shipped.
