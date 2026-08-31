package tests

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"vitals/src/server"
)

// This file is the crash test. Everything else in this directory drives the
// server in process, where a panic is caught and nothing is ever really killed.
// Here the binary is built, run as a child process, fed measurements, and then
// killed without warning, which is the failure the storage design actually
// claims to survive: writes are buffered, so a kill loses at most
// store.FlushInterval and must lose nothing else.
//
// The claim being tested is the one in the README and docs/storage.md. A test
// that only exercised a clean Close would not test it at all.

// buildBinary compiles the tool once for the tests in this file.
func buildBinary(t *testing.T) string {
	t.Helper()

	name := "vitals-crashtest"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	bin := filepath.Join(t.TempDir(), name)

	cmd := exec.Command("go", "build", "-o", bin, "./src/cmd/vitals")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return bin
}

// freePort returns a port nothing is listening on. There is a race between
// closing the listener and the child binding it, which is why the caller waits
// for the child to answer rather than assuming it did.
func freePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startServer runs the binary against dataDir and waits for it to answer.
func startServer(t *testing.T, bin, dataDir string, port int) *exec.Cmd {
	t.Helper()

	cmd := exec.Command(bin,
		"-addr", fmt.Sprintf("127.0.0.1:%d", port),
		"-data", dataDir,
		// The test posts far faster than a browser would, from one address.
		"-rate", "-1",
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the binary: %v", err)
	}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	waitForHealth(t, base)
	return cmd
}

// waitForHealth polls until the child answers or the test gives up.
func waitForHealth(t *testing.T, base string) {
	t.Helper()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the server did not become healthy within 20 seconds")
}

// postN sends n measurements and fails on the first refusal.
func postN(t *testing.T, base string, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		body := fmt.Sprintf(`{"u":"/page-%d","w":1440,"m":{"lcp":%d}}`, i%5, 900+i)
		resp, err := http.Post(base+"/v1/collect", "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatalf("posting measurement %d: %v", i, err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("measurement %d: status = %d, want 204", i, resp.StatusCode)
		}
	}
}

// heldRecords reads the record count out of a running server.
func heldRecords(t *testing.T, base string) int {
	t.Helper()

	resp, err := http.Get(base + "/api/report?from=24h")
	if err != nil {
		t.Fatalf("reading the report: %v", err)
	}
	defer resp.Body.Close()

	var doc struct {
		PageViews int `json:"pageViews"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		t.Fatalf("decoding the report: %v", err)
	}
	return doc.PageViews
}

// TestSurvivesKill is the durability claim, tested against a real process.
//
// The server is fed measurements, given time to flush, then killed without a
// chance to clean up. A second server is started on the same directory and must
// find every flushed record, with a readable log and no manual repair.
func TestSurvivesKill(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped under -short")
	}

	bin := buildBinary(t)
	dataDir := t.TempDir()
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := startServer(t, bin, dataDir, port)

	const sent = 250
	postN(t, base, sent)

	if got := heldRecords(t, base); got != sent {
		t.Fatalf("the running server holds %d records, want %d", got, sent)
	}

	// Longer than store.FlushInterval, so everything sent is on disk and the
	// kill below should cost nothing at all.
	time.Sleep(3 * time.Second)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the server: %v", err)
	}
	_, _ = cmd.Process.Wait()

	// A second process on the same directory. This is the recovery path.
	port2 := freePort(t)
	base2 := fmt.Sprintf("http://127.0.0.1:%d", port2)
	startServer(t, bin, dataDir, port2)

	got := heldRecords(t, base2)
	if got != sent {
		t.Errorf("after the kill the store holds %d records, want all %d; "+
			"nothing was in the buffer at the moment it died", got, sent)
	}
}

// TestSurvivesKillMidWrite kills the process while measurements are still
// arriving, which is the case that can leave a half-written line behind.
//
// The requirement is not that every record survives: the documented guarantee
// is that at most FlushInterval of them are lost. The requirement is that the
// survivors are readable, that the store opens without repair, and that it
// keeps accepting writes afterwards.
func TestSurvivesKillMidWrite(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped under -short")
	}

	bin := buildBinary(t)
	dataDir := t.TempDir()
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := startServer(t, bin, dataDir, port)

	const before = 300
	postN(t, base, before)
	time.Sleep(3 * time.Second) // these are safely on disk

	// A second batch, killed immediately after, so some of it is still in the
	// write buffer when the process dies.
	const during = 200
	postN(t, base, during)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the server: %v", err)
	}
	_, _ = cmd.Process.Wait()

	port2 := freePort(t)
	base2 := fmt.Sprintf("http://127.0.0.1:%d", port2)
	startServer(t, bin, dataDir, port2)

	got := heldRecords(t, base2)

	// The floor is the batch that was definitely flushed. The ceiling is
	// everything sent: losing nothing is a legal outcome, since a flush may
	// have landed between the second batch and the kill.
	if got < before {
		t.Errorf("the store recovered %d records, fewer than the %d that were "+
			"flushed before the kill; the durability guarantee is broken", got, before)
	}
	if got > before+during {
		t.Errorf("the store recovered %d records, more than the %d that were sent",
			got, before+during)
	}
	t.Logf("recovered %d of %d records after a kill mid-write", got, before+during)

	// The store must still be writable, not merely readable.
	postN(t, base2, 10)
	if after := heldRecords(t, base2); after != got+10 {
		t.Errorf("after recovery the store holds %d records, want %d; "+
			"it recovered but stopped accepting writes", after, got+10)
	}
}

// TestLogRemainsParsableAfterKill checks the file itself, not the API. A day
// log whose last line was truncated by a kill must still be a file every other
// line of which parses, because the recovery path skips the broken line rather
// than refusing the file.
func TestLogRemainsParsableAfterKill(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the binary; skipped under -short")
	}

	bin := buildBinary(t)
	dataDir := t.TempDir()
	port := freePort(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", port)

	cmd := startServer(t, bin, dataDir, port)
	postN(t, base, 100)
	time.Sleep(3 * time.Second)

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the server: %v", err)
	}
	_, _ = cmd.Process.Wait()

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatalf("reading the data directory: %v", err)
	}

	logs := 0
	for _, e := range entries {
		if filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		logs++

		raw, err := os.ReadFile(filepath.Join(dataDir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}

		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		for i, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var probe map[string]any
			if err := json.Unmarshal([]byte(line), &probe); err != nil {
				// Only the final line can legitimately be torn.
				if i == len(lines)-1 {
					t.Logf("%s: last line is truncated, which the replay skips", e.Name())
					continue
				}
				t.Errorf("%s line %d does not parse: %v", e.Name(), i+1, err)
			}
		}
	}

	if logs == 0 {
		t.Fatal("the kill left no day log behind at all")
	}
}

// TestServerReportsSkippedLines checks the operator is told when a previous
// process died mid-write, rather than the damage being silently absorbed.
func TestServerReportsSkippedLines(t *testing.T) {
	dir := t.TempDir()

	// A log whose last line was cut off, which is what a kill mid-write leaves.
	day := time.Now().UTC().Format("2006-01-02") + ".jsonl"
	content := `{"t":1756500000000,"u":"/","m":{"lcp":900}}` + "\n" +
		`{"t":1756500001000,"u":"/pricing","m":{"lcp":1200}}` + "\n" +
		`{"t":1756500002000,"u":"/chec`
	if err := os.WriteFile(filepath.Join(dir, day), []byte(content), 0o644); err != nil {
		t.Fatalf("writing the torn log: %v", err)
	}

	app, err := server.Open(dir)
	if err != nil {
		t.Fatalf("opening a store with a torn log: %v", err)
	}
	defer app.Close()

	if got := app.Records(); got != 2 {
		t.Errorf("recovered %d records, want the 2 complete ones", got)
	}
	if got := app.Skipped(); got != 1 {
		t.Errorf("Skipped() = %d, want 1; a torn line must be counted, not hidden", got)
	}
}
