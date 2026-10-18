package mime

import (
	"bytes"
	"errors"
)

var errBase64 = errors.New("invalid base64")

func b64Value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+':
		return 62, true
	case c == '/':
		return 63, true
	}
	return 0, false
}

func a2bBase64(data []byte, strict bool) ([]byte, error) {
	out := make([]byte, 0, (len(data)+3)/4*3)
	quad, pads := 0, 0
	var left byte
	for _, c := range data {
		if c == '=' {
			pads++
			if quad >= 2 && quad+pads <= 4 || !strict {
				continue
			}
			if quad == 1 {
				break
			}
			return nil, errBase64
		}
		v, ok := b64Value(c)
		if !ok {
			if strict {
				return nil, errBase64
			}
			continue
		}
		if pads > 0 && strict {
			return nil, errBase64
		}
		pads = 0
		switch quad {
		case 0:
			quad, left = 1, v
		case 1:
			quad = 2
			out = append(out, left<<2|v>>4)
			left = v & 0x0f
		case 2:
			quad = 3
			out = append(out, left<<4|v>>2)
			left = v & 0x03
		case 3:
			quad = 0
			out = append(out, left<<6|v)
			left = 0
		}
	}
	if quad == 1 || quad != 0 && quad+pads < 4 {
		return nil, errBase64
	}
	return out, nil
}

func decodeB(encoded []byte) []byte {
	var pad []byte
	if r := len(encoded) % 4; r != 0 {
		pad = []byte("===")[:4-r]
	}
	if out, err := a2bBase64(append(encoded[:len(encoded):len(encoded)], pad...), true); err == nil {
		return out
	}
	if out, err := a2bBase64(encoded, false); err == nil {
		return out
	}
	if out, err := a2bBase64(append(encoded[:len(encoded):len(encoded)], '=', '='), false); err == nil {
		return out
	}
	return encoded
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func a2bQP(data []byte, header bool) []byte {
	out := make([]byte, 0, len(data))
	in := 0
	for in < len(data) {
		switch {
		case data[in] == '=':
			in++
			if in >= len(data) {
				return out
			}
			switch {
			case data[in] == '\n' || data[in] == '\r':
				if data[in] != '\n' {
					for in < len(data) && data[in] != '\n' {
						in++
					}
				}
				if in < len(data) {
					in++
				}
			case data[in] == '=':
				out = append(out, '=')
				in++
			case in+1 < len(data) && isHex(data[in]) && isHex(data[in+1]):
				out = append(out, hexValue(data[in])<<4|hexValue(data[in+1]))
				in += 2
			default:
				out = append(out, '=')
			}
		case header && data[in] == '_':
			out = append(out, ' ')
			in++
		default:
			out = append(out, data[in])
			in++
		}
	}
	return out
}

func decodeQ(encoded []byte) []byte {
	encoded = bytes.ReplaceAll(encoded, []byte("_"), []byte(" "))
	out := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] == '=' && i+2 < len(encoded) && isHex(encoded[i+1]) && isHex(encoded[i+2]) {
			out = append(out, hexValue(encoded[i+1])<<4|hexValue(encoded[i+2]))
			i += 2
			continue
		}
		out = append(out, encoded[i])
	}
	return out
}

func splitLinesBytes(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		i := bytes.IndexAny(b, "\r\n")
		if i < 0 {
			out = append(out, b)
			break
		}
		out = append(out, b[:i])
		if b[i] == '\r' && i+1 < len(b) && b[i+1] == '\n' {
			i++
		}
		b = b[i+1:]
	}
	return out
}

func decodeBase64Payload(payload []byte) []byte {
	return decodeB(bytes.Join(splitLinesBytes(payload), nil))
}

var errUU = errors.New("invalid uuencoded data")

func a2bUU(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errUU
	}
	lead := data[0]
	data = data[1:]
	n := int(lead-' ') & 0o77
	out := make([]byte, 0, n)
	var left uint
	bits := 0
	i := 0
	for ; len(out) < n; i++ {
		var c byte
		if i < len(data) && data[i] != '\n' && data[i] != '\r' {
			c = data[i]
			if c < ' ' || c > ' '+64 {
				return nil, errUU
			}
			c = (c - ' ') & 0o77
		}
		left = left<<6 | uint(c)
		bits += 6
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(left>>uint(bits)))
			left &= 1<<uint(bits) - 1
		}
	}
	for ; i < len(data); i++ {
		if c := data[i]; c != ' ' && c != ' '+64 && c != '\n' && c != '\r' {
			return nil, errUU
		}
	}
	return out, nil
}

func isOctalMode(b []byte) bool {
	b = bytes.Trim(b, " \t\n\r\v\f")
	if len(b) > 0 && (b[0] == '+' || b[0] == '-') {
		b = b[1:]
	}
	if len(b) > 1 && b[0] == '0' && (b[1] == 'o' || b[1] == 'O') {
		b = b[2:]
		if len(b) > 0 && b[0] == '_' {
			b = b[1:]
		}
	}
	if len(b) == 0 {
		return false
	}
	prevUnderscore := true
	for _, c := range b {
		switch {
		case c == '_' && !prevUnderscore:
			prevUnderscore = true
		case c >= '0' && c <= '7':
			prevUnderscore = false
		default:
			return false
		}
	}
	return !prevUnderscore
}

func decodeUU(encoded []byte) ([]byte, error) {
	lines := splitLinesBytes(encoded)
	i := 0
	for ; i < len(lines); i++ {
		if rest, ok := bytes.CutPrefix(lines[i], []byte("begin ")); ok {
			mode, _, _ := bytes.Cut(rest, []byte(" "))
			if isOctalMode(mode) {
				break
			}
		}
	}
	if i == len(lines) {
		return nil, errUU
	}
	var out []byte
	for _, line := range lines[i+1:] {
		if len(line) == 0 {
			return nil, errUU
		}
		if string(bytes.Trim(line, " \t\r\n\f")) == "end" {
			break
		}
		dec, err := a2bUU(line)
		if err != nil {
			nbytes := (int((line[0]-32)&63)*4 + 5) / 3
			dec, err = a2bUU(line[:min(nbytes, len(line))])
			if err != nil {
				return nil, err
			}
		}
		out = append(out, dec...)
	}
	return out, nil
}
