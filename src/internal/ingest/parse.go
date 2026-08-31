// Package ingest validates and accepts beacon payloads over HTTP.
//
// The payload parser is hand-written rather than built on encoding/json. This is
// the only place the program reads untrusted input, so the failure modes are
// worth owning: it accepts one object shape, imposes its own length limits, and
// cannot be steered into unbounded work by a hostile body.
package ingest

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"vitals/src/internal/stats"
)

// Payload limits, enforced by the parser itself rather than by a caller.
const (
	// MaxBodyBytes is the largest request body accepted. A well-formed payload
	// is under 200 bytes from the small beacon and under 700 from the full one,
	// which also sends an element selector per metric.
	MaxBodyBytes = 4096
	// MaxRouteBytes caps the stored route. Longer routes are truncated, not
	// rejected: a long URL is a real page, not an attack.
	MaxRouteBytes = 512
	// MaxIDBytes caps the page-view identifier. The beacon generates 13 or 14
	// characters; anything longer than this is not from the beacon.
	MaxIDBytes = 32
	// MaxAttributionBytes caps a stored element selector. Longer values are
	// truncated rather than rejected: a real page can have a very long class
	// name, and the prefix still names the element.
	MaxAttributionBytes = 128
	// maxMetrics bounds map growth from a body full of unknown keys.
	maxMetrics = 32
	// maxAttributes bounds the attribution object the same way.
	maxAttributes = 8
)

// navigationTypes is the closed set of navigation types accepted. The first
// four come from the browser's navigation entry; the last two are the full
// beacon's own labels for a page view that no navigation entry describes.
//
// The value is rendered on the dashboard, so it is matched against this set
// rather than sanitised: an unknown value is dropped, never displayed.
var navigationTypes = map[string]bool{
	"navigate":           true,
	"reload":             true,
	"back_forward":       true,
	"prerender":          true,
	"back-forward-cache": true,
	"soft-navigation":    true,
}

// Parse errors, deliberately coarse: the endpoint answers 204 either way and
// the counters only distinguish malformed from oversized.
var (
	// ErrTooLarge is returned when the body exceeds MaxBodyBytes.
	ErrTooLarge = errors.New("ingest: payload too large")
	// ErrMalformed is returned when the body is not a well-formed payload.
	ErrMalformed = errors.New("ingest: malformed payload")
)

// Payload is one beacon report, as sent by the browser.
type Payload struct {
	// Route is the page path, query string already stripped by the beacon.
	Route string
	// At is the client's epoch-millisecond timestamp, or 0 if not sent. Never
	// trusted for storage; the server's clock is used instead.
	At int64
	// Width is the viewport width in CSS pixels, or 0 if not sent.
	Width int
	// ID is the beacon's per-page-view identifier, or empty if not sent. It
	// exists so a payload delivered twice is stored once; it is not a visitor
	// identifier and does not persist across page views.
	ID string
	// Nav is how the page view began: one of [navigationTypes], or empty when
	// the beacon sent nothing or sent something unrecognised.
	Nav string
	// Attribution names the element responsible for a metric, keyed by the same
	// metric. Only the full beacon sends it, and only for LCP, CLS, and INP.
	Attribution map[stats.Metric]string
	// Values holds metrics already filtered to known keys with finite,
	// non-negative, in-range values.
	Values map[stats.Metric]float64
}

// plausibleMax is the largest value accepted per metric. One tampered sample
// would distort the whole distribution.
var plausibleMax = map[stats.Metric]float64{
	stats.LCP:  600000, // 10 minutes
	stats.INP:  600000,
	stats.FCP:  600000,
	stats.TTFB: 600000,
	stats.CLS:  100, // unitless; real values are under 1, but shifts do stack
}

// Parse decodes a beacon payload, ignoring any key it does not recognise.
func Parse(body []byte) (Payload, error) {
	if len(body) > MaxBodyBytes {
		return Payload{}, ErrTooLarge
	}

	p := &parser{buf: body}
	p.skipSpace()
	if !p.consume('{') {
		return Payload{}, ErrMalformed
	}

	out := Payload{Values: make(map[stats.Metric]float64, len(stats.Metrics))}
	if err := p.object(func(key string) error {
		switch key {
		case "u":
			s, err := p.stringValue()
			if err != nil {
				return err
			}
			out.Route = sanitizeRoute(s)
		case "t":
			n, err := p.numberValue()
			if err != nil {
				return err
			}
			if n > 0 && n < math.MaxInt64 {
				out.At = int64(n)
			}
		case "w":
			n, err := p.numberValue()
			if err != nil {
				return err
			}
			if n > 0 && n < 65536 {
				out.Width = int(n)
			}
		case "i":
			s, err := p.stringValue()
			if err != nil {
				return err
			}
			out.ID = sanitizeID(s)
		case "n":
			s, err := p.stringValue()
			if err != nil {
				return err
			}
			if navigationTypes[s] {
				out.Nav = s
			}
		case "a":
			if out.Attribution == nil {
				out.Attribution = make(map[stats.Metric]string, maxAttributes)
			}
			if err := p.attribution(out.Attribution); err != nil {
				return err
			}
		case "m":
			if err := p.metrics(out.Values); err != nil {
				return err
			}
		default:
			return p.skipValue()
		}
		return nil
	}); err != nil {
		return Payload{}, err
	}

	p.skipSpace()
	if p.pos != len(p.buf) {
		return Payload{}, ErrMalformed // trailing garbage
	}
	if out.Route == "" {
		return Payload{}, ErrMalformed // a measurement without a page is useless
	}
	return out, nil
}

// metrics parses the "m" object into dst, keeping only known metrics with
// finite, non-negative, plausible values.
func (p *parser) metrics(dst map[stats.Metric]float64) error {
	p.skipSpace()
	if !p.consume('{') {
		return ErrMalformed
	}

	seen := 0
	return p.object(func(key string) error {
		seen++
		if seen > maxMetrics {
			return ErrMalformed
		}
		v, err := p.numberValue()
		if err != nil {
			return err
		}

		m := stats.Metric(key)
		if !stats.Valid(m) {
			return nil // unknown metric, dropped
		}
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
			return nil
		}
		if max, ok := plausibleMax[m]; ok && v > max {
			return nil
		}
		dst[m] = v
		return nil
	})
}

// attribution parses the "a" object into dst, keeping only known metric keys
// with a non-empty sanitized value.
func (p *parser) attribution(dst map[stats.Metric]string) error {
	p.skipSpace()
	if !p.consume('{') {
		return ErrMalformed
	}

	seen := 0
	return p.object(func(key string) error {
		seen++
		if seen > maxAttributes {
			return ErrMalformed
		}
		// Read as a string rather than skipping unknown keys generically: a
		// number or null here means the payload did not come from the beacon,
		// and pretending otherwise would hide a client bug.
		v, err := p.stringValue()
		if err != nil {
			return err
		}

		m := stats.Metric(key)
		if !stats.Valid(m) {
			return nil // unknown metric, dropped
		}
		if v := sanitizeAttribution(v); v != "" {
			dst[m] = v
		}
		return nil
	})
}

// sanitizeID keeps the identifier only if it looks like one the beacon
// generated: lowercase base-36, within the length cap. Anything else is
// discarded rather than cleaned, because the value is only ever compared for
// equality and a rejected one costs nothing but a duplicate that would have
// been dropped.
func sanitizeID(s string) string {
	if s == "" || len(s) > MaxIDBytes {
		return ""
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') {
			return ""
		}
	}
	return s
}

// sanitizeAttribution makes an element selector safe to store and to render.
//
// The value is a string chosen by the page, so it is the one field of the
// payload a hostile client fully controls. Control characters are removed so it
// cannot forge a line in the log or a frame on the event stream, invalid UTF-8
// is replaced so it survives JSON encoding, and the length is capped. Angle
// brackets are deliberately kept: the dashboard escapes what it renders, and
// silently mangling a selector would make a real one unrecognisable.
func sanitizeAttribution(s string) string {
	var b []byte
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			// Either an invalid byte or a literal U+FFFD; both encode the same.
			b = utf8.AppendRune(b, utf8.RuneError)
		case r < 0x20 || r == 0x7f:
			// Control characters, including the newline that separates records
			// in the log and the frames on the event stream.
		default:
			b = utf8.AppendRune(b, r)
		}
	}

	out := strings.TrimSpace(string(b))
	if len(out) > MaxAttributionBytes {
		out = out[:MaxAttributionBytes]
		// The cut can land mid-rune.
		for len(out) > 0 && !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}

// sanitizeRoute strips a query string and fragment, enforces a leading slash,
// caps the length, and replaces invalid UTF-8. The route is rendered on the
// dashboard, so it must be safe to store.
func sanitizeRoute(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == '?' || s[i] == '#' {
			s = s[:i]
			break
		}
	}
	if s == "" {
		return ""
	}
	if s[0] != '/' {
		s = "/" + s
	}
	if len(s) > MaxRouteBytes {
		s = s[:MaxRouteBytes]
		// The cut can land mid-rune.
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	if !utf8.ValidString(s) {
		return strconv.QuoteToASCII(s)
	}
	return s
}

// parser is a single-pass scanner over one payload body.
type parser struct {
	buf []byte
	pos int
}

// object consumes members of an object whose opening brace was already read.
// fn must consume exactly that key's value.
func (p *parser) object(fn func(key string) error) error {
	p.skipSpace()
	if p.consume('}') {
		return nil
	}

	for {
		p.skipSpace()
		key, err := p.stringValue()
		if err != nil {
			return err
		}
		p.skipSpace()
		if !p.consume(':') {
			return ErrMalformed
		}
		if err := fn(key); err != nil {
			return err
		}

		p.skipSpace()
		if p.consume(',') {
			continue
		}
		if p.consume('}') {
			return nil
		}
		return ErrMalformed
	}
}

// stringValue reads a JSON string, resolving escape sequences.
func (p *parser) stringValue() (string, error) {
	p.skipSpace()
	if !p.consume('"') {
		return "", ErrMalformed
	}

	start := p.pos // fast path: no escapes, so the bytes can be sliced
	for p.pos < len(p.buf) {
		c := p.buf[p.pos]
		if c == '"' {
			s := string(p.buf[start:p.pos])
			p.pos++
			return s, nil
		}
		if c == '\\' {
			return p.stringWithEscapes(start)
		}
		if c < 0x20 {
			return "", ErrMalformed // raw control character
		}
		p.pos++
	}
	return "", ErrMalformed // unterminated
}

// stringWithEscapes handles the slow path, replaying from start so the result
// includes what the fast path already scanned.
func (p *parser) stringWithEscapes(start int) (string, error) {
	var out []byte
	out = append(out, p.buf[start:p.pos]...)

	for p.pos < len(p.buf) {
		c := p.buf[p.pos]
		switch {
		case c == '"':
			p.pos++
			return string(out), nil
		case c < 0x20:
			return "", ErrMalformed
		case c != '\\':
			out = append(out, c)
			p.pos++
			continue
		}

		p.pos++ // the backslash
		if p.pos >= len(p.buf) {
			return "", ErrMalformed
		}
		esc := p.buf[p.pos]
		p.pos++

		switch esc {
		case '"', '\\', '/':
			out = append(out, esc)
		case 'b':
			out = append(out, '\b')
		case 'f':
			out = append(out, '\f')
		case 'n':
			out = append(out, '\n')
		case 'r':
			out = append(out, '\r')
		case 't':
			out = append(out, '\t')
		case 'u':
			r, err := p.unicodeEscape()
			if err != nil {
				return "", err
			}
			out = utf8.AppendRune(out, r)
		default:
			return "", ErrMalformed
		}
	}
	return "", ErrMalformed
}

// unicodeEscape reads a \u escape, pairing surrogates where a valid pair
// follows and substituting U+FFFD for a lone one.
func (p *parser) unicodeEscape() (rune, error) {
	r, err := p.hex4()
	if err != nil {
		return 0, err
	}

	if !utf16.IsSurrogate(r) {
		return r, nil
	}
	if p.pos+1 < len(p.buf) && p.buf[p.pos] == '\\' && p.buf[p.pos+1] == 'u' {
		save := p.pos
		p.pos += 2
		lo, err := p.hex4()
		if err != nil {
			return 0, err
		}
		if combined := utf16.DecodeRune(r, lo); combined != utf8.RuneError {
			return combined, nil
		}
		p.pos = save
	}
	return utf8.RuneError, nil
}

// hex4 reads exactly four hexadecimal digits.
func (p *parser) hex4() (rune, error) {
	if p.pos+4 > len(p.buf) {
		return 0, ErrMalformed
	}
	var r rune
	for i := 0; i < 4; i++ {
		c := p.buf[p.pos+i]
		var d rune
		switch {
		case c >= '0' && c <= '9':
			d = rune(c - '0')
		case c >= 'a' && c <= 'f':
			d = rune(c-'a') + 10
		case c >= 'A' && c <= 'F':
			d = rune(c-'A') + 10
		default:
			return 0, ErrMalformed
		}
		r = r<<4 | d
	}
	p.pos += 4
	return r, nil
}

// numberValue reads a JSON number. JSON has no NaN or Infinity literal, so the
// result is finite by construction.
func (p *parser) numberValue() (float64, error) {
	p.skipSpace()
	start := p.pos

	if p.pos < len(p.buf) && (p.buf[p.pos] == '-' || p.buf[p.pos] == '+') {
		p.pos++
	}
	for p.pos < len(p.buf) {
		c := p.buf[p.pos]
		if (c >= '0' && c <= '9') || c == '.' || c == 'e' || c == 'E' || c == '+' || c == '-' {
			p.pos++
			continue
		}
		break
	}
	if p.pos == start {
		return 0, ErrMalformed
	}

	v, err := strconv.ParseFloat(string(p.buf[start:p.pos]), 64)
	if err != nil {
		// An overflowing exponent yields ErrRange, treated as malformed rather
		// than stored as an infinity.
		return 0, fmt.Errorf("%w: %v", ErrMalformed, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, ErrMalformed
	}
	return v, nil
}

// skipValue discards any JSON value, so unknown keys do not break parsing.
// Nesting is bounded because the body is.
func (p *parser) skipValue() error {
	p.skipSpace()
	if p.pos >= len(p.buf) {
		return ErrMalformed
	}

	switch c := p.buf[p.pos]; {
	case c == '"':
		_, err := p.stringValue()
		return err
	case c == '{' || c == '[':
		return p.skipContainer()
	case c == 't':
		return p.literal("true")
	case c == 'f':
		return p.literal("false")
	case c == 'n':
		return p.literal("null")
	default:
		_, err := p.numberValue()
		return err
	}
}

// skipContainer consumes a balanced object or array, respecting strings so a
// brace inside one does not confuse the depth count.
func (p *parser) skipContainer() error {
	depth := 0
	for p.pos < len(p.buf) {
		switch p.buf[p.pos] {
		case '"':
			if _, err := p.stringValue(); err != nil {
				return err
			}
			continue
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				p.pos++
				return nil
			}
		}
		p.pos++
	}
	return ErrMalformed
}

// literal consumes an exact keyword.
func (p *parser) literal(want string) error {
	if p.pos+len(want) > len(p.buf) || string(p.buf[p.pos:p.pos+len(want)]) != want {
		return ErrMalformed
	}
	p.pos += len(want)
	return nil
}

// consume advances past c if it is the next non-space byte.
func (p *parser) consume(c byte) bool {
	p.skipSpace()
	if p.pos < len(p.buf) && p.buf[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

// skipSpace advances past JSON whitespace.
func (p *parser) skipSpace() {
	for p.pos < len(p.buf) {
		switch p.buf[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}
