package mime

// Compat32Header is str(email.message_from_bytes(header).get(name)) under policy.compat32 for a header block:
// the first field named name with only its first line's leading spaces and tabs and its trailing newlines
// stripped, folding kept. surrogates reports undecodable bytes in it: Python then returns a Header whose str
// keeps them as lone surrogates, so the value equals no decoded string; value has them as U+FFFD.
func Compat32Header(header []byte, name string) (value string, found, surrogates bool) {
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
		return "", false, false
	}
	v := rstripNewlines(lstripSet(f.source, " \t"))
	return sanitize(v), true, hasSurrogates(v)
}
