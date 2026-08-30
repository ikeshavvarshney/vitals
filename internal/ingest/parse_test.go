package ingest

import (
	"errors"
	"strings"
	"testing"

	"vitals/internal/stats"
)

func TestParseValid(t *testing.T) {
	body := `{"u":"/pricing","t":1756500000000,"w":1440,"m":{"lcp":1834.2,"cls":0.06,"inp":142,"ttfb":210.5,"fcp":903.1}}`

	got, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if got.Route != "/pricing" {
		t.Errorf("Route = %q, want %q", got.Route, "/pricing")
	}
	if got.At != 1756500000000 {
		t.Errorf("At = %d, want 1756500000000", got.At)
	}
	if got.Width != 1440 {
		t.Errorf("Width = %d, want 1440", got.Width)
	}

	want := map[stats.Metric]float64{
		stats.LCP: 1834.2, stats.CLS: 0.06, stats.INP: 142,
		stats.TTFB: 210.5, stats.FCP: 903.1,
	}
	if len(got.Values) != len(want) {
		t.Fatalf("got %d metrics, want %d: %v", len(got.Values), len(want), got.Values)
	}
	for m, v := range want {
		if got.Values[m] != v {
			t.Errorf("Values[%s] = %v, want %v", m, got.Values[m], v)
		}
	}
}

func TestParseMalformed(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"empty body", ``},
		{"only whitespace", `   `},
		{"not an object", `"hello"`},
		{"array at top level", `[1,2,3]`},
		{"bare number", `42`},
		{"unclosed object", `{"u":"/"`},
		{"unclosed string", `{"u":"/pric`},
		{"missing colon", `{"u" "/"}`},
		{"missing comma", `{"u":"/" "w":100}`},
		{"trailing comma", `{"u":"/",}`},
		{"trailing garbage after object", `{"u":"/","m":{}} extra`},
		{"two objects", `{"u":"/","m":{}}{"u":"/x","m":{}}`},
		{"unquoted key", `{u:"/"}`},
		{"single quotes", `{'u':'/'}`},
		{"no route", `{"t":1756500000000,"m":{"lcp":1000}}`},
		{"empty route", `{"u":"","m":{"lcp":1000}}`},
		{"route that is only a query string", `{"u":"?a=b","m":{}}`},
		{"metrics is not an object", `{"u":"/","m":123}`},
		{"metrics is an array", `{"u":"/","m":[1,2]}`},
		{"raw control character in string", "{\"u\":\"/a\tb\",\"m\":{}}"},
		{"invalid escape", `{"u":"/a\qb","m":{}}`},
		{"truncated unicode escape", `{"u":"/a\u12","m":{}}`},
		{"non-hex unicode escape", `{"u":"/a\uZZZZ","m":{}}`},
		{"metric value is a string", `{"u":"/","m":{"lcp":"fast"}}`},
		{"metric value is null", `{"u":"/","m":{"lcp":null}}`},
		{"number with no digits", `{"u":"/","w":-,"m":{}}`},
		{"exponent overflows to infinity", `{"u":"/","m":{"lcp":1e400}}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Parse([]byte(tt.body)); err == nil {
				t.Errorf("Parse(%q) = nil error, want an error", tt.body)
			}
		})
	}
}

func TestParseTooLarge(t *testing.T) {
	body := `{"u":"/` + strings.Repeat("a", MaxBodyBytes) + `","m":{}}`

	_, err := Parse([]byte(body))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("Parse(oversized) = %v, want ErrTooLarge", err)
	}
}

func TestParseAtSizeLimit(t *testing.T) {
	// A body exactly at the limit is accepted; one byte more is not.
	prefix := `{"u":"/`
	suffix := `","m":{}}`
	pad := MaxBodyBytes - len(prefix) - len(suffix)

	atLimit := prefix + strings.Repeat("a", pad) + suffix
	if len(atLimit) != MaxBodyBytes {
		t.Fatalf("fixture is %d bytes, want %d", len(atLimit), MaxBodyBytes)
	}
	if _, err := Parse([]byte(atLimit)); err != nil {
		t.Errorf("Parse at exactly MaxBodyBytes: %v, want success", err)
	}

	overLimit := prefix + strings.Repeat("a", pad+1) + suffix
	if _, err := Parse([]byte(overLimit)); !errors.Is(err, ErrTooLarge) {
		t.Errorf("Parse at MaxBodyBytes+1 = %v, want ErrTooLarge", err)
	}
}

func TestParseFiltersMetrics(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantValues map[stats.Metric]float64
	}{
		{
			name:       "unknown metric dropped",
			body:       `{"u":"/","m":{"lcp":1000,"fid":88,"nonsense":5}}`,
			wantValues: map[stats.Metric]float64{stats.LCP: 1000},
		},
		{
			name:       "negative value dropped",
			body:       `{"u":"/","m":{"lcp":-1,"cls":0.05}}`,
			wantValues: map[stats.Metric]float64{stats.CLS: 0.05},
		},
		{
			name:       "implausibly large duration dropped",
			body:       `{"u":"/","m":{"lcp":999999999,"fcp":900}}`,
			wantValues: map[stats.Metric]float64{stats.FCP: 900},
		},
		{
			name:       "implausibly large cls dropped",
			body:       `{"u":"/","m":{"cls":500,"lcp":1000}}`,
			wantValues: map[stats.Metric]float64{stats.LCP: 1000},
		},
		{
			name:       "zero is a legitimate value",
			body:       `{"u":"/","m":{"cls":0}}`,
			wantValues: map[stats.Metric]float64{stats.CLS: 0},
		},
		{
			name:       "empty metrics object is valid",
			body:       `{"u":"/","m":{}}`,
			wantValues: map[stats.Metric]float64{},
		},
		{
			name:       "missing metrics key is valid",
			body:       `{"u":"/"}`,
			wantValues: map[stats.Metric]float64{},
		},
		{
			name:       "duplicate key keeps the last value",
			body:       `{"u":"/","m":{"lcp":1000,"lcp":2000}}`,
			wantValues: map[stats.Metric]float64{stats.LCP: 2000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if len(got.Values) != len(tt.wantValues) {
				t.Fatalf("got %v, want %v", got.Values, tt.wantValues)
			}
			for m, v := range tt.wantValues {
				if got.Values[m] != v {
					t.Errorf("Values[%s] = %v, want %v", m, got.Values[m], v)
				}
			}
		})
	}
}

func TestParseTooManyMetricKeys(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"u":"/","m":{`)
	for i := 0; i < maxMetrics+5; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`"k`)
		b.WriteString(strings.Repeat("x", 1))
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(`":1`)
	}
	b.WriteString(`}}`)

	if _, err := Parse([]byte(b.String())); err == nil {
		t.Error("Parse with more than maxMetrics keys returned nil, want an error")
	}
}

func TestParseIgnoresUnknownTopLevelKeys(t *testing.T) {
	tests := []string{
		`{"u":"/","zz":123,"m":{"lcp":1000}}`,
		`{"u":"/","zz":"a string","m":{"lcp":1000}}`,
		`{"u":"/","zz":{"nested":{"deep":[1,2,3]}},"m":{"lcp":1000}}`,
		`{"u":"/","zz":[1,"two",{"three":3}],"m":{"lcp":1000}}`,
		`{"u":"/","zz":null,"m":{"lcp":1000}}`,
		`{"u":"/","zz":true,"m":{"lcp":1000}}`,
		`{"u":"/","zz":false,"m":{"lcp":1000}}`,
		`{"u":"/","zz":"a } brace in a string","m":{"lcp":1000}}`,
	}

	for _, body := range tests {
		got, err := Parse([]byte(body))
		if err != nil {
			t.Errorf("Parse(%s): %v", body, err)
			continue
		}
		if got.Values[stats.LCP] != 1000 {
			t.Errorf("Parse(%s) lost the lcp value: %v", body, got.Values)
		}
	}
}

func TestParseWhitespaceTolerant(t *testing.T) {
	body := "{\n  \"u\" : \"/docs\" ,\n  \"w\" : 800 ,\n  \"m\" : { \"lcp\" : 1500 }\n}\n"

	got, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Route != "/docs" || got.Width != 800 || got.Values[stats.LCP] != 1500 {
		t.Errorf("got %+v, want route /docs, width 800, lcp 1500", got)
	}
}

func TestParseStringEscapes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"escaped slash", `{"u":"\/a\/b","m":{}}`, "/a/b"},
		{"escaped quote", `{"u":"/a\"b","m":{}}`, `/a"b`},
		{"escaped backslash", `{"u":"/a\\b","m":{}}`, `/a\b`},
		{"unicode escape", `{"u":"/café","m":{}}`, "/café"},
		{"surrogate pair", `{"u":"/😀","m":{}}`, "/\U0001F600"},
		{"raw utf-8 passes through", `{"u":"/café/über","m":{}}`, "/café/über"},
		{"tab escape", `{"u":"/a\tb","m":{}}`, "/a\tb"},
		{"newline escape", `{"u":"/a\nb","m":{}}`, "/a\nb"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.Route != tt.want {
				t.Errorf("Route = %q, want %q", got.Route, tt.want)
			}
		})
	}
}

func TestParseUnpairedSurrogateDoesNotPanic(t *testing.T) {
	// A lone high surrogate is not a character. It must be substituted, not
	// crash the parser and not produce invalid UTF-8 in the store.
	got, err := Parse([]byte(`{"u":"/\ud83d","m":{}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got.Route == "" {
		t.Error("Route is empty, want a substituted rune")
	}
}

func TestSanitizeRoute(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"already clean", "/pricing", "/pricing"},
		{"query string stripped", "/search?q=hello&page=2", "/search"},
		{"fragment stripped", "/docs#section-3", "/docs"},
		{"query before fragment", "/a?b=c#d", "/a"},
		{"leading slash added", "pricing", "/pricing"},
		{"root", "/", "/"},
		{"empty stays empty", "", ""},
		{"only a query string becomes empty", "?a=b", ""},
		{"trailing slash preserved", "/docs/", "/docs/"},
		{"unicode preserved", "/café", "/café"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeRoute(tt.in); got != tt.want {
				t.Errorf("sanitizeRoute(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSanitizeRouteTruncates(t *testing.T) {
	long := "/" + strings.Repeat("a", MaxRouteBytes*2)
	got := sanitizeRoute(long)

	if len(got) > MaxRouteBytes {
		t.Errorf("route is %d bytes, want at most %d", len(got), MaxRouteBytes)
	}
	if !strings.HasPrefix(got, "/aaa") {
		t.Errorf("truncated route = %q, want it to keep the prefix", got[:10])
	}
}

func TestSanitizeRouteTruncationKeepsValidUTF8(t *testing.T) {
	// Fill the route with multi-byte runes so the cut lands mid-rune.
	long := "/" + strings.Repeat("é", MaxRouteBytes)
	got := sanitizeRoute(long)

	if len(got) > MaxRouteBytes {
		t.Errorf("route is %d bytes, want at most %d", len(got), MaxRouteBytes)
	}
	for _, r := range got {
		if r == '�' {
			t.Error("truncation produced an invalid rune")
			break
		}
	}
}

func TestParseTimestampAndWidthBounds(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantAt    int64
		wantWidth int
	}{
		{"normal", `{"u":"/","t":1756500000000,"w":1440,"m":{}}`, 1756500000000, 1440},
		{"negative timestamp ignored", `{"u":"/","t":-5,"w":1440,"m":{}}`, 0, 1440},
		{"zero timestamp ignored", `{"u":"/","t":0,"w":1440,"m":{}}`, 0, 1440},
		{"negative width ignored", `{"u":"/","t":1,"w":-800,"m":{}}`, 1, 0},
		{"zero width ignored", `{"u":"/","t":1,"w":0,"m":{}}`, 1, 0},
		{"absurd width ignored", `{"u":"/","t":1,"w":999999,"m":{}}`, 1, 0},
		{"fractional width truncated", `{"u":"/","t":1,"w":1440.7,"m":{}}`, 1, 1440},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.body))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if got.At != tt.wantAt {
				t.Errorf("At = %d, want %d", got.At, tt.wantAt)
			}
			if got.Width != tt.wantWidth {
				t.Errorf("Width = %d, want %d", got.Width, tt.wantWidth)
			}
		})
	}
}

// FuzzParse asserts the only property that really matters for untrusted input:
// no input, however malformed, may panic the parser.
func FuzzParse(f *testing.F) {
	seeds := []string{
		`{"u":"/pricing","t":1756500000000,"w":1440,"m":{"lcp":1834.2}}`,
		`{"u":"/","m":{}}`,
		`{}`,
		``,
		`{"u":"/\ud83d","m":{}}`,
		`{"u":"/","zz":{"a":[1,{"b":2}]},"m":{"cls":0.1}}`,
		`{"u":"/","m":{"lcp":1e308}}`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, body []byte) {
		p, err := Parse(body)
		if err != nil {
			return
		}
		// A successful parse must produce values that are safe to store.
		if p.Route == "" {
			t.Errorf("Parse succeeded with an empty route: %q", body)
		}
		for m, v := range p.Values {
			if !stats.Valid(m) {
				t.Errorf("Parse kept an unknown metric %q", m)
			}
			if v < 0 {
				t.Errorf("Parse kept a negative value for %s: %v", m, v)
			}
		}
	})
}
