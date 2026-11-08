package textutil

import (
	"bytes"
	"io"
	"strings"

	"golang.org/x/net/html/charset"
)

func Decode(data []byte, label string) string {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "utf-8"
	}
	r, err := charset.NewReaderLabel(label, bytes.NewReader(data))
	if err != nil {
		return ReplaceUTF8(data)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		return ReplaceUTF8(data)
	}
	return string(out)
}

func ReplaceUTF8(data []byte) string {
	return strings.ToValidUTF8(string(data), "\uFFFD")
}
