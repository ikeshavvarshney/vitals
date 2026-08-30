package tests

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"vitals/src/server"
)

// TestStorageUsageIsReported covers the figures the dashboard's storage panel
// reads: they must describe the files on disk, not an estimate.
func TestStorageUsageIsReported(t *testing.T) {
	srv, app := newServer(t)
	for i := 0; i < 5; i++ {
		seed(t, srv, "/", 1440, map[string]float64{"lcp": 1500})
	}

	usage, err := app.Usage()
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if usage.Records != 5 {
		t.Errorf("Records = %d, want 5", usage.Records)
	}

	var summary struct {
		Coverage struct {
			Total          int     `json:"total"`
			Bytes          int64   `json:"bytes"`
			Files          int     `json:"files"`
			BytesPerRecord float64 `json:"bytesPerRecord"`
			RetentionDays  float64 `json:"retentionDays"`
		} `json:"coverage"`
	}
	getJSON(t, srv.URL+"/api/summary?from=24h", &summary)

	if summary.Coverage.Total != 5 {
		t.Errorf("coverage total = %d, want 5", summary.Coverage.Total)
	}
	if summary.Coverage.Files != 1 {
		t.Errorf("coverage files = %d, want 1", summary.Coverage.Files)
	}
	if summary.Coverage.Bytes <= 0 {
		t.Errorf("coverage bytes = %d, want the day log's size", summary.Coverage.Bytes)
	}
	if got := summary.Coverage.BytesPerRecord; got < 1 || got > 1000 {
		t.Errorf("bytesPerRecord = %v, want a plausible per-record cost", got)
	}
	if summary.Coverage.RetentionDays != 0 {
		t.Errorf("retentionDays = %v, want 0 when nothing is dropped", summary.Coverage.RetentionDays)
	}
}

// TestRetentionDropsOldDayLogs covers the -retain flag end to end: an old day
// log is gone from disk, from memory, and from the API, and today's is not.
func TestRetentionDropsOldDayLogs(t *testing.T) {
	dir := t.TempDir()

	// A day log from well before the retention window, written by hand in the
	// on-disk format so the test does not depend on being able to backdate a
	// beacon.
	old := time.Now().UTC().AddDate(0, 0, -10)
	name := filepath.Join(dir, old.Format("2006-01-02")+".jsonl")
	line := fmt.Sprintf(`{"t":%d,"u":"/ancient","w":1440,"m":{"lcp":1500}}`+"\n", old.UnixMilli())
	if err := os.WriteFile(name, []byte(line), 0o644); err != nil {
		t.Fatalf("writing old day log: %v", err)
	}

	app, err := server.OpenWith(dir, server.Options{Retention: 48 * time.Hour})
	if err != nil {
		t.Fatalf("OpenWith: %v", err)
	}
	defer app.Close()

	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Errorf("old day log still on disk: %v", err)
	}
	if app.Records() != 0 {
		t.Errorf("records = %d, want the old day dropped from memory", app.Records())
	}

	srv := httptest.NewServer(app.Handler())
	defer srv.Close()

	// A fresh measurement is unaffected, and retention is reported.
	seed(t, srv, "/current", 1440, map[string]float64{"lcp": 1200})

	var summary struct {
		Samples  int `json:"samples"`
		Coverage struct {
			Total         int     `json:"total"`
			RetentionDays float64 `json:"retentionDays"`
		} `json:"coverage"`
	}
	getJSON(t, srv.URL+"/api/summary?from=24h", &summary)

	if summary.Samples != 1 || summary.Coverage.Total != 1 {
		t.Errorf("samples = %d, total = %d; want only the fresh measurement",
			summary.Samples, summary.Coverage.Total)
	}
	if summary.Coverage.RetentionDays != 2 {
		t.Errorf("retentionDays = %v, want 2", summary.Coverage.RetentionDays)
	}

	// The dashboard must be able to say the storage panel is there at all.
	_, body := fetch(t, srv.URL+"/", nil)
	if !strings.Contains(body, `id="storage"`) {
		t.Error("dashboard has no storage panel")
	}
	if resp, _ := fetch(t, srv.URL+"/", nil); resp.StatusCode != http.StatusOK {
		t.Errorf("dashboard status = %d, want 200", resp.StatusCode)
	}
}
