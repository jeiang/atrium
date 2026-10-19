package mime

import (
	"errors"

	"github.com/jeiang/atrium/internal/py"
)

func decodeNamed(data []byte, charset string, mode errMode) (string, error) {
	out, err := decodeBytes(data, charset, mode)
	switch {
	case errors.Is(err, errLookup):
		return "", &py.Exception{Class: "LookupError", Msg: "unknown encoding: " + charset}
	case errors.Is(err, errNul):
		return "", &py.Exception{Class: "ValueError", Msg: "embedded null character"}
	case errors.Is(err, errDecode):
		return "", &py.Exception{Class: "UnicodeDecodeError", Msg: "'" + charset + "' codec can't decode the bytes"}
	case err != nil:
		return "", err
	}
	return string(out), nil
}

// DecodeReplace is data.decode(charset, errors="replace") for a codec Python ships: undecodable bytes become
// U+FFFD, an unknown codec name is a LookupError and a name with a NUL a ValueError.
func DecodeReplace(data []byte, charset string) (string, error) {
	return decodeNamed(data, charset, errReplace)
}

// ContentCharset is Message.get_content_charset() for a Content-Type header value: the lower-cased charset
// parameter, an RFC 2231 one decoded in the charset it names. ok is false when there is none or it is not ASCII.
func ContentCharset(contentType string) (charset string, ok bool) {
	for _, p := range decodeParams(parseParams(contentType)) {
		if Lower(p.name) != "charset" {
			continue
		}
		charset = unquote(p.value.text)
		if p.value.tuple {
			name := p.value.charset
			if !p.value.hasCharset || name == "" {
				name = "us-ascii"
			}
			raw := make([]byte, 0, len(charset))
			for _, r := range charset {
				raw = append(raw, byte(r))
			}
			if decoded, err := decodeNamed(raw, name, errStrict); err == nil {
				charset = decoded
			}
		}
		for _, r := range charset {
			if r >= 0x80 {
				return "", false
			}
		}
		return Lower(charset), true
	}
	return "", false
}
