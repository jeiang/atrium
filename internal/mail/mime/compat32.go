package mime

import "unicode/utf8"

// Compat32Header is str(email.message_from_bytes(header).get(name)) under policy.compat32 for a header block:
// the first field named name, its raw value with folding kept. A value with undecodable bytes is a Header in
// the unknown-8bit charset, whose str turns each such byte into U+FFFD.
func Compat32Header(header []byte, name string) (string, bool) {
	r := &lineReader{lines: splitLines(header)}
	var lines [][]byte
	for {
		line, ok := r.readline()
		if !ok || !isHeaderLine(line) {
			break
		}
		lines = append(lines, line)
	}
	m := &Message{}
	m.parseHeaders(lines, r)
	f, ok := m.find(name)
	if !ok {
		return "", false
	}
	out := make([]rune, len(f.value))
	for i, c := range f.value {
		if isSurrogate(c) {
			c = utf8.RuneError
		}
		out[i] = c
	}
	return string(out), true
}
