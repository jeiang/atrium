package contacts

import (
	"bytes"
	"strings"

	"github.com/jeiang/atrium/internal/textutil"
)

func ReadText(data []byte) string {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	s := textutil.ReplaceUTF8(data)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}
