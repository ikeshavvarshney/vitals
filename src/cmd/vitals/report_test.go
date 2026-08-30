package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedDir writes one day log holding the given measurements.
func seedDir(t *testing.T, lines ...string) string {
	t.Helper()

	dir := t.TempDir()
	name := filepath.Join(dir, time.Now().UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(name, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing day log: %v", err)
	}
	return dir
}

// measurement builds one on-disk line, stamped now.
func measurement(route string, lcp float64) string {
	return fmt.Sprintf(`{"t":%d,"u":%q,"w":1440,"m":{"lcp":%v,"cls":0.02}}`,
		time.Now().UTC().UnixMilli(), route, lcp)
}

func TestReportCommandPrintsATable(t *testing.T) {
	dir := seedDir(t, measurement("/slow", 6000), measurement("/", 900))

	var out bytes.Buffer
	if err := reportCommand([]string{"-data", dir, "-window", "24h"}, &out); err != nil {
		t.Fatalf("reportCommand: %v", err)
	}
	got := out.String()

	for _, want := range []string{"LCP", "p75", "/slow", "2 page view", "Storage", "approximate"} {
		if !strings.Contains(got, want) {
			t.Errorf("output does not mention %q\n%s", want, got)
		}
	}
	// The slowest route is the actionable line; it must name the slow one.
	if !strings.Contains(got, "/slow") {
		t.Errorf("slowest route missing from:\n%s", got)
	}
}

func TestReportCommandJSONIsTheAPIDocument(t *testing.T) {
	dir := seedDir(t, measurement("/", 1200))

	var out bytes.Buffer
	if err := reportCommand([]string{"-data", dir, "-json"}, &out); err != nil {
		t.Fatalf("reportCommand: %v", err)
	}

	var doc struct {
		PageViews  int     `json:"pageViews"`
		Percentile float64 `json:"headlinePercentile"`
		Metrics    []struct {
			Metric    string             `json:"metric"`
			Quantiles map[string]float64 `json:"quantiles"`
		} `json:"metrics"`
		Caveats []string `json:"caveats"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out.String())
	}

	if doc.PageViews != 1 {
		t.Errorf("pageViews = %d, want 1", doc.PageViews)
	}
	if doc.Percentile != 0.75 {
		t.Errorf("headlinePercentile = %v, want 0.75", doc.Percentile)
	}
	if len(doc.Caveats) == 0 {
		t.Error("caveats missing; the printed document must carry its disclosures")
	}
	if len(doc.Metrics) != 5 {
		t.Errorf("metrics = %d, want 5", len(doc.Metrics))
	}
}

func TestReportCommandHonoursPercentileAndRoute(t *testing.T) {
	dir := seedDir(t,
		measurement("/fast", 500), measurement("/fast", 500), measurement("/fast", 500),
		measurement("/slow", 9000),
	)

	var wide, filtered bytes.Buffer
	if err := reportCommand([]string{"-data", dir, "-p", "95"}, &wide); err != nil {
		t.Fatalf("reportCommand: %v", err)
	}
	if !strings.Contains(wide.String(), "p95") {
		t.Errorf("p95 not reported:\n%s", wide.String())
	}

	if err := reportCommand([]string{"-data", dir, "-route", "/slow"}, &filtered); err != nil {
		t.Fatalf("reportCommand: %v", err)
	}
	got := filtered.String()
	if !strings.Contains(got, "route   /slow") {
		t.Errorf("filtered report does not name the route:\n%s", got)
	}
	if !strings.Contains(got, "1 page view") {
		t.Errorf("filtered report counts more than the one matching view:\n%s", got)
	}
}

func TestReportCommandRejectsAnUnlistedPercentile(t *testing.T) {
	dir := seedDir(t, measurement("/", 1000))

	var out bytes.Buffer
	err := reportCommand([]string{"-data", dir, "-p", "99"}, &out)
	if err == nil {
		t.Fatal("want an error for p=99, got none")
	}
	if !strings.Contains(err.Error(), "p:") {
		t.Errorf("error = %v, want it to name the parameter", err)
	}
}

func TestRunSubcommandLeavesServingAlone(t *testing.T) {
	var out bytes.Buffer

	if handled, _ := runSubcommand(nil, &out); handled {
		t.Error("no arguments must start the server, not a subcommand")
	}
	if handled, _ := runSubcommand([]string{"-addr", ":9000"}, &out); handled {
		t.Error("a server flag must not be read as a subcommand")
	}
	if handled, err := runSubcommand([]string{"help"}, &out); !handled || err != nil {
		t.Errorf("help: handled = %v, err = %v", handled, err)
	}
	if !strings.Contains(out.String(), "vitals report") {
		t.Errorf("help does not mention the report command:\n%s", out.String())
	}
}
