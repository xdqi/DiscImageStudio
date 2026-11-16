package bluray

import (
	"strconv"
	"strings"
	"time"
)

// jsonWriter renders JSON byte-identically to System.Text.Json with
// WriteIndented = true on Linux: two-space indent, '\n' newlines, a space
// after the colon, and the default JavaScriptEncoder string escaping.
//
// Usage: within an object call Property(name) then the Value*/Begin* method
// for the value; within an array call Element() then the value method.
type jsonWriter struct {
	buf     strings.Builder
	pending []bool // per nesting level: true while the container is empty
}

func newJSONWriter() *jsonWriter { return &jsonWriter{} }

func (w *jsonWriter) String() string { return w.buf.String() }
func (w *jsonWriter) depth() int     { return len(w.pending) }

// prelude writes the comma separator and newline+indent for the next element.
func (w *jsonWriter) prelude() {
	if w.depth() == 0 {
		return
	}
	if w.pending[w.depth()-1] {
		w.pending[w.depth()-1] = false
	} else {
		w.buf.WriteByte(',')
	}
	w.buf.WriteByte('\n')
	for i := 0; i < w.depth(); i++ {
		w.buf.WriteString("  ")
	}
}

// Property writes the member prelude, the escaped key and ": ".
func (w *jsonWriter) Property(name string) {
	w.prelude()
	stjEscapeString(&w.buf, name)
	w.buf.WriteString(": ")
}

// Element writes the element prelude inside an array.
func (w *jsonWriter) Element() { w.prelude() }

func (w *jsonWriter) BeginObject() {
	w.buf.WriteByte('{')
	w.pending = append(w.pending, true)
}

func (w *jsonWriter) EndObject() {
	empty := w.pending[w.depth()-1]
	w.pending = w.pending[:w.depth()-1]
	if !empty {
		w.buf.WriteByte('\n')
		for i := 0; i < w.depth(); i++ {
			w.buf.WriteString("  ")
		}
	}
	w.buf.WriteByte('}')
}

func (w *jsonWriter) BeginArray() {
	w.buf.WriteByte('[')
	w.pending = append(w.pending, true)
}

func (w *jsonWriter) EndArray() {
	empty := w.pending[w.depth()-1]
	w.pending = w.pending[:w.depth()-1]
	if !empty {
		w.buf.WriteByte('\n')
		for i := 0; i < w.depth(); i++ {
			w.buf.WriteString("  ")
		}
	}
	w.buf.WriteByte(']')
}

func (w *jsonWriter) StringValue(v string) { stjEscapeString(&w.buf, v) }

func (w *jsonWriter) BoolValue(v bool) {
	if v {
		w.buf.WriteString("true")
	} else {
		w.buf.WriteString("false")
	}
}

func (w *jsonWriter) Null() { w.buf.WriteString("null") }

func (w *jsonWriter) Uint32Value(v uint32) { w.buf.WriteString(strconv.FormatUint(uint64(v), 10)) }

func (w *jsonWriter) IntValue(v int) { w.buf.WriteString(strconv.Itoa(v)) }

func (w *jsonWriter) Int64Value(v int64) { w.buf.WriteString(strconv.FormatInt(v, 10)) }

// DoubleValue ports System.Text.Json's double rendering: shortest round-trip
// digits with an uppercase 'E' exponent marker.
func (w *jsonWriter) DoubleValue(v float64) { w.buf.WriteString(dotnetFloat(v)) }

// dotnetFloat formats like System.Text.Json for the finite doubles used here.
func dotnetFloat(v float64) string {
	s := strconv.FormatFloat(v, 'g', -1, 64)
	if i := strings.IndexByte(s, 'e'); i >= 0 {
		s = s[:i] + "E" + s[i+1:]
	}
	return s
}

// DateTimeOffsetValue ports the System.Text.Json DateTimeOffset rendering:
// "yyyy-MM-ddTHH:mm:ss" plus up to seven fractional digits (trailing zeros
// trimmed) and the explicit numeric offset ("+00:00" for UTC).
func (w *jsonWriter) DateTimeOffsetValue(t time.Time) {
	w.buf.WriteByte('"')
	w.buf.WriteString(t.Format("2006-01-02T15:04:05"))
	if fraction := formatTicksFraction(t.Nanosecond() / 100); fraction != "" {
		w.buf.WriteString(fraction)
	}
	w.buf.WriteString(t.Format("-07:00"))
	w.buf.WriteByte('"')
}

// formatTicksFraction renders 100-ns ticks as ".fffffff" with trailing zeros
// trimmed; empty when zero.
func formatTicksFraction(ticks int) string {
	if ticks == 0 {
		return ""
	}
	digits := []byte("0000000")
	for i := 6; i >= 0 && ticks > 0; i-- {
		digits[i] = byte('0' + ticks%10)
		ticks /= 10
	}
	end := 7
	for digits[end-1] == '0' {
		end--
	}
	return "." + string(digits[:end])
}

// stjEscapeString writes a JSON string with the escaping of System.Text.Json's
// default JavaScriptEncoder: control characters, '"', '\', '\”, '<', '>',
// '&', '+' and all non-ASCII as \uXXXX (uppercase, UTF-16 code units).
func stjEscapeString(buf *strings.Builder, s string) {
	const hexDigits = "0123456789ABCDEF"
	buf.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\\':
			buf.WriteString("\\\\")
		case r == '"':
			buf.WriteString("\\u0022")
		case r == '\'' || r == '<' || r == '>' || r == '&' || r == '+':
			buf.WriteString("\\u00")
			buf.WriteByte(hexDigits[(r>>4)&0xF])
			buf.WriteByte(hexDigits[r&0xF])
		case r < 0x20 || r > 0x7E:
			if r > 0xFFFF {
				r -= 0x10000
				hi := 0xD800 + (r >> 10)
				lo := 0xDC00 + (r & 0x3FF)
				for _, u := range [2]rune{hi, lo} {
					buf.WriteString("\\u")
					for shift := 12; shift >= 0; shift -= 4 {
						buf.WriteByte(hexDigits[(u>>shift)&0xF])
					}
				}
			} else {
				buf.WriteString("\\u")
				for shift := 12; shift >= 0; shift -= 4 {
					buf.WriteByte(hexDigits[(r>>shift)&0xF])
				}
			}
		default:
			buf.WriteByte(byte(r))
		}
	}
	buf.WriteByte('"')
}
