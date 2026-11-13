package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"

	"github.com/jeiang/atrium/internal/application"
)

const (
	cursorVersion = 1
	CursorMax     = 2048
	LimitDefault  = 50
	LimitMax      = 100
)

type Limit int

type Cursor struct {
	op     application.Name
	filter string
	last   json.RawMessage
}

type cursorWire struct {
	V      int              `json:"v"`
	Op     application.Name `json:"op"`
	Filter string           `json:"filter"`
	Last   json.RawMessage  `json:"last"`
}

func (c Cursor) Set() bool { return len(c.last) > 0 }

var ErrBadCursor = errors.New("bad cursor")

func (c Cursor) Last(v any) error {
	dec := json.NewDecoder(bytes.NewReader(c.last))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return ErrBadCursor
	}
	if valid, ok := v.(interface{ Valid() bool }); ok && !valid.Valid() {
		return ErrBadCursor
	}
	return nil
}

func (c Cursor) Next(last any) *string {
	raw, err := json.Marshal(last)
	if err != nil {
		panic(err)
	}
	wire, err := json.Marshal(cursorWire{V: cursorVersion, Op: c.op, Filter: c.filter, Last: raw})
	if err != nil {
		panic(err)
	}
	s := base64.RawURLEncoding.EncodeToString(wire)
	return &s
}

func filterOf(query url.Values) string {
	rest := url.Values{}
	for k, v := range query {
		if k != "cursor" && k != "limit" {
			rest[k] = v
		}
	}
	sum := sha256.Sum256([]byte(rest.Encode()))
	return hex.EncodeToString(sum[:])
}

func parseCursor(op application.Name, query url.Values, raw string) (Cursor, bool) {
	c := Cursor{op: op, filter: filterOf(query)}
	if raw == "" {
		return c, true
	}
	if len(raw) > CursorMax {
		return Cursor{}, false
	}
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return Cursor{}, false
	}
	if op == "notifications.inbox" && c.filter == filterOf(url.Values{}) && json.Valid(data) {
		var legacy struct {
			ID int64 `json:"id"`
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if dec.Decode(&legacy) == nil && legacy.ID > 0 {
			c.last = data
			return c, true
		}
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var wire cursorWire
	if dec.Decode(&wire) != nil || wire.V != cursorVersion || wire.Op != op || wire.Filter != c.filter || len(wire.Last) == 0 {
		return Cursor{}, false
	}
	c.last = wire.Last
	return c, true
}
