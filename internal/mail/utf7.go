package mail

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf16"

	"github.com/jeiang/atrium/internal/rx"
)

var utf7ShiftRE = rx.MustCompile(`&([^-]*)-`, 0)

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// UTF7Decode is imap.utf7_decode: modified UTF-7 (RFC 3501 5.1.3) to text.
func UTF7Decode(value string) (string, error) {
	var failed error
	out, err := utf7ShiftRE.SubFunc(value, func(m *rx.Match) string {
		chunk := m.Group(1)
		if chunk == "" {
			return "&"
		}
		chunk = strings.ReplaceAll(chunk, ",", "/")
		chunk += strings.Repeat("=", (4-len([]rune(chunk))%4)%4)
		text, err := decodeUTF16Base64(chunk)
		if err != nil && failed == nil {
			failed = err
		}
		return text
	}, 0)
	if err != nil {
		return "", err
	}
	return out, failed
}

// binasciiError is binascii.Error.
type binasciiError string

func (e binasciiError) Error() string { return string(e) }

// decodeUTF16Base64 is base64.b64decode (binascii's non-strict a2b_base64) then bytes.decode("utf-16-be").
func decodeUTF16Base64(s string) (string, error) {
	for _, r := range s {
		if r > 0x7f {
			return "", errors.New("string argument should contain only ASCII characters")
		}
	}
	var out []byte
	quad, left, pads := 0, 0, 0
	data := 0
	done := false
	for i := range len(s) {
		c := s[i]
		if c == '=' {
			pads++
			if quad >= 2 && quad+pads >= 4 {
				done = true
				break
			}
			continue
		}
		v := strings.IndexByte(b64Alphabet, c)
		if v < 0 {
			continue
		}
		pads = 0
		data++
		switch quad {
		case 0:
			quad, left = 1, v
		case 1:
			quad = 2
			out = append(out, byte(left<<2|v>>4))
			left = v & 0x0f
		case 2:
			quad = 3
			out = append(out, byte(left<<4|v>>2))
			left = v & 0x03
		default:
			quad = 0
			out = append(out, byte(left<<6|v))
		}
	}
	if !done && quad == 1 {
		return "", binasciiError(fmt.Sprintf("Invalid base64-encoded string: number of data characters (%d) cannot be 1 more than a multiple of 4", data))
	}
	if !done && quad != 0 {
		return "", binasciiError("Incorrect padding")
	}
	if len(out)%2 != 0 {
		return "", fmt.Errorf("'utf-16-be' codec can't decode byte 0x%02x in position %d: truncated data", out[len(out)-1], len(out)-1)
	}
	units := make([]uint16, len(out)/2)
	for i := range units {
		units[i] = uint16(out[2*i])<<8 | uint16(out[2*i+1])
	}
	for i := 0; i < len(units); i++ {
		switch u := units[i]; {
		case u >= 0xd800 && u < 0xdc00:
			if i+1 >= len(units) {
				return "", fmt.Errorf("'utf-16-be' codec can't decode bytes in position %d-%d: unexpected end of data", 2*i, 2*i+1)
			}
			if units[i+1] < 0xdc00 || units[i+1] > 0xdfff {
				return "", fmt.Errorf("'utf-16-be' codec can't decode bytes in position %d-%d: illegal UTF-16 surrogate", 2*i, 2*i+1)
			}
			i++
		case u >= 0xdc00 && u <= 0xdfff:
			return "", fmt.Errorf("'utf-16-be' codec can't decode bytes in position %d-%d: illegal encoding", 2*i, 2*i+1)
		}
	}
	return string(utf16.Decode(units)), nil
}

// UTF7Encode is imap.utf7_encode: text to modified UTF-7.
func UTF7Encode(value string) string {
	var out strings.Builder
	var pending []rune
	flush := func() {
		if len(pending) == 0 {
			return
		}
		units := utf16.Encode(pending)
		raw := make([]byte, 2*len(units))
		for i, u := range units {
			raw[2*i], raw[2*i+1] = byte(u>>8), byte(u)
		}
		chunk := strings.TrimRight(base64.StdEncoding.EncodeToString(raw), "=")
		out.WriteString("&" + strings.ReplaceAll(chunk, "/", ",") + "-")
		pending = pending[:0]
	}
	for _, ch := range value {
		switch {
		case ch == '&':
			flush()
			out.WriteString("&-")
		case ch >= 0x20 && ch <= 0x7e:
			flush()
			out.WriteRune(ch)
		default:
			pending = append(pending, ch)
		}
	}
	flush()
	return out.String()
}

// CollapseLabels is imap.collapse_labels: the user labels (no leading backslash) that have no sub-label in the
// set, sorted.
func CollapseLabels(labels []string) []string {
	user := map[string]bool{}
	for _, l := range labels {
		if !strings.HasPrefix(l, `\`) {
			user[l] = true
		}
	}
	out := []string{}
	for l := range user {
		leaf := true
		for other := range user {
			if other != l && strings.HasPrefix(other, l+"/") {
				leaf = false
				break
			}
		}
		if leaf {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return out
}
