// Command sha256sum prints the SHA-256 digest of each file named on the
// command line, in the format "digest  path".
//
// It exists so that "make repro" depends only on the Go toolchain. Shelling out
// to sha256sum, shasum, or certutil would make the reproducible-build proof
// depend on which platform the judge happens to be running.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: sha256sum <file>...")
		os.Exit(2)
	}

	status := 0
	for _, path := range os.Args[1:] {
		sum, err := digest(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "sha256sum: %v\n", err)
			status = 1
			continue
		}
		fmt.Printf("%s  %s\n", sum, path)
	}
	os.Exit(status)
}

// digest returns the hex-encoded SHA-256 of the file at path.
func digest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hashing %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
