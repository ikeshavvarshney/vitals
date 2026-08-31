# vitals build targets.
#
# Everything here runs through the Go toolchain. No external tool is invoked,
# which is why "make repro" and "make check" work identically on Linux, macOS,
# and Windows without anyone installing anything.

ifeq ($(OS),Windows_NT)
BIN := vitals.exe
else
BIN := vitals
endif

# -trimpath strips local filesystem paths, -buildid= clears the build ID, and
# nothing injects a timestamp or a git SHA. Those three facts are what make the
# output byte-identical across rebuilds.

.PHONY: all build run report test bench race check beacon compare proof repro clean

all: build

## build: compile the single binary
build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o $(BIN) ./src/cmd/vitals

## run: build, then serve the dashboard and demo site on :8080
run: build
	./$(BIN)

## report: print the collected measurements without a browser
report: build
	./$(BIN) report -window 24h

## test: run the full test suite
test:
	go test ./...

## bench: print the storage benchmarks
#
# The numbers published in docs/storage.md come from this target. They are the
# evidence behind the scale limit stated there, which is otherwise an opinion.
bench:
	go test ./src/internal/store/ -bench . -benchmem -run '^$$'

## race: run the test suite under the race detector, inside Docker
#
# The race detector needs an external linker and therefore a C toolchain, which
# not every machine has. This borrows Linux's. It is the only target that needs
# anything outside the Go toolchain, it is a test-time convenience rather than
# part of the build, and CI runs the same command natively.
race:
	docker run --rm -v "$(CURDIR)":/src -w /src golang:1.23 go test -race ./...

## check: fail on any dependency violation
check:
	go run ./src/tools/check .

## beacon: print the beacon size and enforce the 1KB budget
beacon:
	go run ./src/tools/beaconsize

## compare: print beacon size beside any scripts given in FILES
#
# Not part of the build and it fetches nothing. Download a competitor's script
# first, then: make compare FILES="web-vitals.iife.js"
compare:
	go run ./src/tools/compare $(FILES)

## proof: regenerate deps-proof.txt
proof:
	@echo "# go.mod" > deps-proof.txt
	@cat go.mod >> deps-proof.txt
	@echo "" >> deps-proof.txt
	@echo "# go list -m all" >> deps-proof.txt
	@go list -m all >> deps-proof.txt
	@echo "" >> deps-proof.txt
	@echo "# go version" >> deps-proof.txt
	@go version >> deps-proof.txt
	@cat deps-proof.txt

## repro: build twice and print both SHA-256 hashes
repro:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o vitals.repro1 ./src/cmd/vitals
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags="-s -w -buildid=" -o vitals.repro2 ./src/cmd/vitals
	@go run ./src/tools/sha256sum vitals.repro1 vitals.repro2

## clean: remove build output
clean:
	go clean
	-rm -f $(BIN) vitals.repro1 vitals.repro2
