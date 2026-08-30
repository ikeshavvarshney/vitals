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

.PHONY: all build run test check proof repro clean

all: build

## build: compile the single binary
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o $(BIN) ./cmd/vitals

## run: build, then serve the dashboard and demo site on :8080
run: build
	./$(BIN)

## test: run the full test suite
test:
	go test ./...

## check: fail on any dependency violation
check:
	go run ./tools/check .

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
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o vitals.repro1 ./cmd/vitals
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -buildid=" -o vitals.repro2 ./cmd/vitals
	@go run ./tools/sha256sum vitals.repro1 vitals.repro2

## clean: remove build output
clean:
	go clean
	-rm -f $(BIN) vitals.repro1 vitals.repro2
