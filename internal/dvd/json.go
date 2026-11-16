package dvd

import (
	"strings"
	"unicode/utf16"
)

// stjWriter writes JSON byte-identically to System.Text.Json with
// WriteIndented = true (two-space indent, LF newlines) and the default
// JavaScriptEncoder escaping.
type stjWriter struct {
	buf   strings.Builder
	stack []bool // per-level: true after the first member
}

func (w *stjWriter) depth() int { return len(w.stack) }

func (w *stjWriter) writeIndent() {
	for i := 0; i < w.depth(); i++ {
		w.buf.WriteString("  ")
	}
}

// beforeValue emits the separator before a member value or array element.
// For object members the caller writes `"name": ` via fieldName first.
func (w *stjWriter) beforeElement() {
	if len(w.stack) == 0 {
		return
	}
	if w.stack[len(w.stack)-1] {
		w.buf.WriteString(",\n")
	} else {
		w.buf.WriteByte('\n')
	}
	w.stack[len(w.stack)-1] = true
	w.writeIndent()
}

func (w *stjWriter) fieldName(name string) {
	w.beforeElement()
	writeJsonString(&w.buf, name)
	w.buf.WriteString(": ")
}

func (w *stjWriter) beginObject() {
	w.buf.WriteByte('{')
	w.stack = append(w.stack, false)
}

func (w *stjWriter) endObject() {
	nonEmpty := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	if nonEmpty {
		w.buf.WriteByte('\n')
		w.writeIndent()
	} else {
		w.buf.WriteByte(' ')
	}
	w.buf.WriteByte('}')
}

func (w *stjWriter) beginArray() {
	w.buf.WriteByte('[')
	w.stack = append(w.stack, false)
}

func (w *stjWriter) endArray() {
	nonEmpty := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	if nonEmpty {
		w.buf.WriteByte('\n')
		w.writeIndent()
	} else {
		w.buf.WriteByte(' ')
	}
	w.buf.WriteByte(']')
}

func (w *stjWriter) objectField(name string, write func()) {
	w.fieldName(name)
	w.beginObject()
	write()
	w.endObject()
}

func (w *stjWriter) arrayField(name string, write func()) {
	w.fieldName(name)
	w.beginArray()
	write()
	w.endArray()
}

func (w *stjWriter) stringField(name, value string) {
	w.fieldName(name)
	writeJsonString(&w.buf, value)
}

func (w *stjWriter) stringFieldOrNull(name string, value *string) {
	if value == nil {
		w.nullField(name)
		return
	}
	w.stringField(name, *value)
}

func (w *stjWriter) nullField(name string) {
	w.fieldName(name)
	w.buf.WriteString("null")
}

func (w *stjWriter) boolField(name string, value bool) {
	w.fieldName(name)
	if value {
		w.buf.WriteString("true")
	} else {
		w.buf.WriteString("false")
	}
}

func (w *stjWriter) intField(name string, value int) {
	w.fieldName(name)
	w.buf.WriteString(itoa(int64(value)))
}

func (w *stjWriter) uintField(name string, value uint32) {
	w.fieldName(name)
	w.buf.WriteString(itoa(int64(value)))
}

func (w *stjWriter) uintFieldOrNull(name string, value *uint32) {
	if value == nil {
		w.nullField(name)
		return
	}
	w.uintField(name, *value)
}

func (w *stjWriter) intFieldOrNull(name string, value *int) {
	if value == nil {
		w.nullField(name)
		return
	}
	w.intField(name, *value)
}

func (w *stjWriter) longField(name string, value int64) {
	w.fieldName(name)
	w.buf.WriteString(itoa(value))
}

func (w *stjWriter) longFieldOrNull(name string, value *int64) {
	if value == nil {
		w.nullField(name)
		return
	}
	w.longField(name, *value)
}

func (w *stjWriter) doubleField(name string, value float64) {
	w.fieldName(name)
	w.buf.WriteString(formatDoubleG(value))
}

func (w *stjWriter) doubleFieldOrNull(name string, value *float64) {
	if value == nil {
		w.nullField(name)
		return
	}
	w.doubleField(name, *value)
}

// Array element writers (inside an array context).
func (w *stjWriter) arrayObject(write func()) {
	w.beforeElement()
	w.beginObject()
	write()
	w.endObject()
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	negative := v < 0
	var digits [20]byte
	i := len(digits)
	u := uint64(v)
	if negative {
		u = uint64(-(v + 1)) + 1
	}
	for u > 0 {
		i--
		digits[i] = byte('0' + u%10)
		u /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}

// writeJsonString escapes like System.Text.Json's default JavaScriptEncoder:
// short escapes for \\, \", \b, \t, \n, \f, \r; \u00XX for other controls and
// the HTML-sensitive characters; \uXXXX (surrogate pairs) beyond ASCII.
func writeJsonString(buf *strings.Builder, value string) {
	buf.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			buf.WriteString(`\\`)
		case '"':
			buf.WriteString(`\"`)
		case '\b':
			buf.WriteString(`\b`)
		case '\t':
			buf.WriteString(`\t`)
		case '\n':
			buf.WriteString(`\n`)
		case '\f':
			buf.WriteString(`\f`)
		case '\r':
			buf.WriteString(`\r`)
		default:
			switch {
			case r < 0x20 || r == 0x7F:
				writeUnicodeEscape(buf, uint32(r))
			case r < 0x7F && !isJsonSafeAscii(byte(r)):
				writeUnicodeEscape(buf, uint32(r))
			case r < 0x80:
				buf.WriteByte(byte(r))
			case r <= 0xFFFF:
				writeUnicodeEscape(buf, uint32(r))
			default:
				pair := utf16.Encode([]rune{r})
				for _, u := range pair {
					writeUnicodeEscape(buf, uint32(u))
				}
			}
		}
	}
	buf.WriteByte('"')
}

func isJsonSafeAscii(c byte) bool {
	// JavaScriptEncoder.Default allowlist within printable ASCII: everything
	// except the HTML/JS-sensitive characters.
	switch c {
	case '<', '>', '&', '\'', '`', '+':
		return false
	}
	return true
}

func writeUnicodeEscape(buf *strings.Builder, code uint32) {
	const hexDigits = "0123456789ABCDEF"
	buf.WriteString(`\u`)
	for shift := 12; shift >= 0; shift -= 4 {
		buf.WriteByte(hexDigits[(code>>shift)&0xF])
	}
}
