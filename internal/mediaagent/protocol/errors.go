package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

const MaxBody = 1 << 20

const (
	MutationNone    = "none"
	MutationUnknown = "unknown"
	MutationPartial = "partial"
)

const (
	CodeInvalidRequest    = "invalid_request"
	CodeUnsafePath        = "unsafe_path"
	CodeUnauthorized      = "unauthorized"
	CodeForbidden         = "forbidden"
	CodeNotFound          = "not_found"
	CodeMethodNotAllowed  = "method_not_allowed"
	CodeKeyReused         = "key_reused"
	CodeDestinationExists = "destination_exists"
	CodeIdentityChanged   = "identity_changed"
	CodeRootChanged       = "root_changed"
	CodeBusy              = "busy"
	CodeSnapshotExpired   = "snapshot_expired"
	CodeRequestTooLarge   = "request_too_large"
	CodeCrossDevice       = "cross_device"
	CodeUnsupportedFile   = "unsupported_file"
	CodePermissions       = "permissions"
	CodeUnavailable       = "unavailable"
	CodeDraining          = "draining"
	CodeIOError           = "io_error"
	CodeReceiptInvalid    = "receipt_invalid"
)

type Problem struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Mutation string `json:"mutation"`
}

func (p *Problem) Error() string { return p.Code + ": " + p.Message }

var (
	ErrTooLarge     = errors.New("the request body is too large")
	ErrDuplicateKey = errors.New("the request body repeats a key")
	ErrTrailingData = errors.New("the request body has data after the value")
)

func Decode(r io.Reader, limit int64, v any) error {
	if limit <= 0 || limit > MaxBody {
		limit = MaxBody
	}
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > limit {
		return ErrTooLarge
	}
	walk := json.NewDecoder(bytes.NewReader(body))
	walk.UseNumber()
	if err := uniqueKeys(walk, 0); err != nil {
		return err
	}
	if _, err := walk.Token(); !errors.Is(err, io.EOF) {
		return ErrTrailingData
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func uniqueKeys(dec *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("the request body is nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		var seen []string
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return fmt.Errorf("the request body has a non-string key")
			}
			for _, prior := range seen {
				if strings.EqualFold(prior, name) {
					return fmt.Errorf("%w: %q", ErrDuplicateKey, name)
				}
			}
			seen = append(seen, name)
			if err := uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := uniqueKeys(dec, depth+1); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token()
	return err
}
