package mime

import "unicode/utf8"

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
