package tests

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seed posts one measurement and returns nothing; a failure fails the test.
func seed(t *testing.T, srv *httptest.Server, route string, width int, values map[string]float64) {
	t.Helper()

	pairs := make([]string, 0, len(values))
	for k, v := range values {
		pairs = append(pairs, fmt.Sprintf("%q:%v", k, v))
	}
	body := fmt.Sprintf(`{"u":%q,"w":%d,"m":{%s}}`, route, width, strings.Join(pairs, ","))

	resp, err := http.Post(srv.URL+"/v1/collect", "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatalf("posting %s: %v", route, err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("collect status = %d, want 204", resp.StatusCode)
	}
}

// TestSummaryContract pins the shape the dashboard and any other consumer read.
// The figure is called "value" rather than "p75" because the percentile is
// selectable; a rename here is a breaking change and should fail loudly.
func TestSummaryContract(t *testing.T) {
	srv, _ := newServer(t)
	seed(t, srv, "/pricing", 1440, map[string]float64{"lcp": 2100, "cls": 0.04, "inp": 180})

	var summary struct {
		Samples    int     `json:"samples"`
		Percentile float64 `json:"percentile"`
		Metrics    []struct {
			Metric           string   `json:"metric"`
			Value            *float64 `json:"value"`
			Band             string   `json:"band"`
			Samples          uint64   `json:"samples"`
			Previous         *float64 `json:"previous"`
			Good             float64  `json:"good"`
			NeedsImprovement float64  `json:"needsImprovement"`
			Unit             string   `json:"unit"`
		} `json:"metrics"`
		Compared *struct {
			Samples int `json:"samples"`
		} `json:"compared"`
		Coverage *struct {
			Total int `json:"total"`
		} `json:"coverage"`
		BeaconBytes int `json:"beaconBytes"`
	}
	getJSON(t, srv.URL+"/api/summary?from=24h", &summary)

	if summary.Samples != 1 {
		t.Errorf("samples = %d, want 1", summary.Samples)
	}
	if summary.Percentile != 0.75 {
		t.Errorf("percentile = %v, want 0.75 by default", summary.Percentile)
	}
	if len(summary.Metrics) != 5 {
		t.Fatalf("metrics = %d, want all five reported", len(summary.Metrics))
	}
	if summary.Compared == nil {
		t.Error("compared is absent; the scorecard cannot show movement without it")
	}
	if summary.Coverage == nil || summary.Coverage.Total != 1 {
		t.Errorf("coverage = %+v, want a total of 1", summary.Coverage)
	}
	if summary.BeaconBytes <= 0 {
		t.Errorf("beaconBytes = %d, want the served size", summary.BeaconBytes)
	}

	for _, m := range summary.Metrics {
		switch m.Metric {
		case "lcp":
			if m.Value == nil || *m.Value < 2000 || *m.Value > 2200 {
				t.Errorf("lcp value = %v, want about 2100", m.Value)
			}
			if m.Band != "good" || m.Unit != "ms" {
				t.Errorf("lcp band = %q, unit = %q; want good and ms", m.Band, m.Unit)
			}
		case "cls":
			if m.Unit != "" {
				t.Errorf("cls unit = %q, want empty; the score is unitless", m.Unit)
			}
		case "fcp", "ttfb":
			// Not reported by this payload, and absent must not read as zero.
			if m.Value != nil {
				t.Errorf("%s value = %v, want null when unreported", m.Metric, *m.Value)
			}
			if m.Band != "" {
				t.Errorf("%s band = %q, want empty when unreported", m.Metric, m.Band)
			}
		}
	}
}

func TestPercentileAndRouteFilterOverHTTP(t *testing.T) {
	srv, _ := newServer(t)
	for i := 0; i < 9; i++ {
		seed(t, srv, "/fast", 1440, map[string]float64{"lcp": 500})
	}
	seed(t, srv, "/slow", 390, map[string]float64{"lcp": 8000})

	read := func(query string) (float64, int) {
		t.Helper()
		var resp struct {
			Samples int `json:"samples"`
			Metrics []struct {
				Metric string   `json:"metric"`
				Value  *float64 `json:"value"`
			} `json:"metrics"`
		}
		getJSON(t, srv.URL+"/api/summary?"+query, &resp)
		for _, m := range resp.Metrics {
			if m.Metric == "lcp" && m.Value != nil {
				return *m.Value, resp.Samples
			}
		}
		t.Fatalf("%s: no lcp value", query)
		return 0, 0
	}

	p50, _ := read("from=24h&p=50")
	p95, _ := read("from=24h&p=95")
	if p50 >= p95 {
		t.Errorf("p50 = %v, p95 = %v; the tail must be at least as slow", p50, p95)
	}

	filtered, samples := read("from=24h&route=%2Fslow")
	if samples != 1 {
		t.Errorf("filtered samples = %d, want 1", samples)
	}
	if filtered < 7000 {
		t.Errorf("filtered lcp = %v, want the slow route's own figure", filtered)
	}
}

func TestReportCarriesItsOwnCaveats(t *testing.T) {
	srv, _ := newServer(t)
	seed(t, srv, "/", 1440, map[string]float64{"lcp": 1200, "inp": 90})

	var report struct {
		PageViews int `json:"pageViews"`
		Metrics   []struct {
			Metric       string             `json:"metric"`
			Quantiles    map[string]float64 `json:"quantiles"`
			Distribution struct {
				Good             uint64 `json:"good"`
				NeedsImprovement uint64 `json:"needsImprovement"`
				Poor             uint64 `json:"poor"`
			} `json:"distribution"`
			WorstRoutes []struct {
				Key string `json:"key"`
			} `json:"worstRoutes"`
		} `json:"metrics"`
		Caveats []string `json:"caveats"`
	}
	getJSON(t, srv.URL+"/api/report?from=24h", &report)

	if report.PageViews != 1 {
		t.Errorf("pageViews = %d, want 1", report.PageViews)
	}
	if len(report.Caveats) == 0 {
		t.Error("caveats is empty; a copied report must carry its disclosures")
	}

	for _, m := range report.Metrics {
		if m.Metric != "lcp" {
			continue
		}
		for _, q := range []string{"p50", "p75", "p90", "p95"} {
			if _, ok := m.Quantiles[q]; !ok {
				t.Errorf("lcp quantiles missing %q", q)
			}
		}
		if m.Distribution.Good != 1 {
			t.Errorf("lcp good count = %d, want 1", m.Distribution.Good)
		}
		if len(m.WorstRoutes) != 1 || m.WorstRoutes[0].Key != "/" {
			t.Errorf("worstRoutes = %+v, want one row for /", m.WorstRoutes)
		}
	}
}

// TestBadParametersAreRejected covers the promise that a parameter the server
// cannot read is an error rather than a silently different window.
func TestBadParametersAreRejected(t *testing.T) {
	srv, _ := newServer(t)

	bad := []string{
		"/api/summary?from=yesterday",
		"/api/summary?from=24", // a duration without a unit
		"/api/summary?p=99",    // not a selectable percentile
		"/api/summary?p=0.9",   // a fraction rather than a percentage
		"/api/series?metric=xyz",
		"/api/series?from=24h&n=0",
		"/api/series?from=24h&n=100000",
	}

	for _, target := range bad {
		t.Run(target, func(t *testing.T) {
			resp, err := http.Get(srv.URL + target)
			if err != nil {
				t.Fatalf("GET %s: %v", target, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", resp.StatusCode)
			}
			if body := readBody(t, resp); !strings.Contains(body, `"error"`) {
				t.Errorf("body = %q, want a JSON error naming the parameter", body)
			}
		})
	}
}
