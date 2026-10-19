package mime

import (
	"errors"

	"github.com/jeiang/atrium/internal/py"
)

// LookupError is Python's LookupError for a codec name that does not exist; the text is str(e).
type LookupError string

func (e LookupError) Error() string { return string(e) }
func (LookupError) PyClass() string { return "LookupError" }

// DecodeError is Python's UnicodeDecodeError: the bytes are not valid in the codec.
type DecodeError string

func (e DecodeError) Error() string { return string(e) }
func (DecodeError) PyClass() string { return "UnicodeDecodeError" }

func decodeNamed(data []byte, charset string, mode errMode) (string, error) {
	out, err := decodeBytes(data, charset, mode)
	switch {
	case errors.Is(err, errLookup):
		return "", LookupError("unknown encoding: " + charset)
	case errors.Is(err, errNul):
		return "", py.ValueError("embedded null character")
	case errors.Is(err, errDecode):
		return "", DecodeError("'" + charset + "' codec can't decode the bytes")
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

// DecodeStrict is data.decode(charset): undecodable bytes are a DecodeError.
func DecodeStrict(data []byte, charset string) (string, error) {
	return decodeNamed(data, charset, errStrict)
}
