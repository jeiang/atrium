package py

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
)

const b64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// BinasciiError is binascii.Error.
type BinasciiError string

func (e BinasciiError) Error() string { return string(e) }

// B64Decode is base64.b64decode(s) for a str: binascii's non-strict a2b_base64, which skips characters outside
// the alphabet and stops at complete padding.
func B64Decode(s string) ([]byte, error) {
	for _, r := range s {
		if r > 0x7f {
			return nil, errors.New("string argument should contain only ASCII characters")
		}
	}
	var out []byte
	quad, left, pads, data := 0, 0, 0, 0
	for i := range len(s) {
		c := s[i]
		if c == '=' {
			pads++
			if quad >= 2 && quad+pads >= 4 {
				return out, nil
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
	switch quad {
	case 0:
		return out, nil
	case 1:
		return nil, BinasciiError(fmt.Sprintf("Invalid base64-encoded string: number of data characters (%d) cannot be 1 more than a multiple of 4", data))
	}
	return nil, BinasciiError("Incorrect padding")
}

// DecodeUTF16BE is bytes.decode("utf-16-be") with strict errors.
func DecodeUTF16BE(b []byte) (string, error) {
	if len(b)%2 != 0 {
		return "", fmt.Errorf("'utf-16-be' codec can't decode byte 0x%02x in position %d: truncated data", b[len(b)-1], len(b)-1)
	}
	units := make([]uint16, len(b)/2)
	for i := range units {
		units[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
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
