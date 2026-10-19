package py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Pair is one key and value of an Object.
type Pair struct {
	Key   string
	Value any
}

// Object is a JSON object whose keys keep their order, like a Python dict. A map[string]any dumps with its
// keys sorted, because Go maps have no order.
type Object []Pair

// JSONOpts selects json.dumps arguments: EnsureASCII (the Python default) escapes every non-ASCII code
// point, Compact uses separators (",", ":") instead of (", ", ": "), SortKeys sorts Object keys too.
type JSONOpts struct {
	EnsureASCII bool
	Compact     bool
	SortKeys    bool
}

// intLit is an integer literal that was parsed from JSON text; Python's int has no size limit, so it stays text.
type intLit string

// JSONDumps is json.dumps(v, ...) for v built from nil, bool, string, integers, floats, json.Number,
// json.RawMessage, []any, []string, []json.RawMessage, map[string]any and Object. A json.RawMessage is
// parsed and dumped again, so its spacing, string escapes and number spelling become Python's.
func JSONDumps(v any, o JSONOpts) (string, error) {
	var b bytes.Buffer
	if err := dump(&b, v, o); err != nil {
		return "", err
	}
	return b.String(), nil
}

// JSONRedump is json.dumps(json.loads(raw), ...): the same value with Python's spacing, string escapes and
// number spelling, keys in their original order.
func JSONRedump(raw []byte, o JSONOpts) (string, error) {
	return JSONDumps(json.RawMessage(raw), o)
}

func dump(b *bytes.Buffer, v any, o JSONOpts) error {
	itemSep, keySep := ", ", ": "
	if o.Compact {
		itemSep, keySep = ",", ":"
	}
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		b.Write(AppendJSONString(nil, x, o.EnsureASCII))
	case intLit:
		b.WriteString(string(x))
	case int:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int32:
		b.WriteString(strconv.FormatInt(int64(x), 10))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case uint64:
		b.WriteString(strconv.FormatUint(x, 10))
	case float32:
		b.WriteString(jsonFloat(float64(x)))
	case float64:
		b.WriteString(jsonFloat(x))
	case json.Number:
		return dump(b, numberOf(x), o)
	case json.RawMessage:
		tree, err := parseJSON(x)
		if err != nil {
			return err
		}
		return dump(b, tree, o)
	case []byte:
		return errors.New("py: json: []byte is not JSON; use json.RawMessage")
	case []string:
		items := make([]any, len(x))
		for i, s := range x {
			items[i] = s
		}
		return dump(b, items, o)
	case []json.RawMessage:
		items := make([]any, len(x))
		for i, r := range x {
			items[i] = r
		}
		return dump(b, items, o)
	case []any:
		b.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				b.WriteString(itemSep)
			}
			if err := dump(b, item, o); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		pairs := make(Object, 0, len(x))
		for k, item := range x {
			pairs = append(pairs, Pair{k, item})
		}
		slices.SortFunc(pairs, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		return dump(b, pairs, o)
	case Object:
		pairs := x
		if o.SortKeys {
			pairs = slices.Clone(x)
			slices.SortStableFunc(pairs, func(a, c Pair) int { return strings.Compare(a.Key, c.Key) })
		}
		b.WriteByte('{')
		for i, p := range pairs {
			if i > 0 {
				b.WriteString(itemSep)
			}
			b.Write(AppendJSONString(nil, p.Key, o.EnsureASCII))
			b.WriteString(keySep)
			if err := dump(b, p.Value, o); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("py: json: cannot dump %s", reflect.TypeOf(v))
	}
	return nil
}

func jsonFloat(x float64) string {
	switch {
	case math.IsNaN(x):
		return "NaN"
	case math.IsInf(x, 1):
		return "Infinity"
	case math.IsInf(x, -1):
		return "-Infinity"
	}
	return FloatRepr(x)
}

// numberOf is how json.loads reads a number literal: an integer stays exact, anything else is a float.
func numberOf(n json.Number) any {
	s := string(n)
	if !strings.ContainsAny(s, ".eE") {
		if strings.TrimLeft(s, "-") == "0" {
			return intLit("0")
		}
		return intLit(s)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !math.IsInf(f, 0) {
		return intLit(s)
	}
	return f
}

func parseJSON(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, fmt.Errorf("py: json: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("py: json: extra data after the value")
	}
	return v, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			items := []any{}
			for dec.More() {
				item, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				items = append(items, item)
			}
			_, err := dec.Token()
			return items, err
		}
		obj := Object{}
		index := map[string]int{}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			item, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			if at, dup := index[key]; dup {
				obj[at].Value = item
				continue
			}
			index[key] = len(obj)
			obj = append(obj, Pair{key, item})
		}
		_, err = dec.Token()
		return obj, err
	case json.Number:
		return numberOf(t), nil
	default:
		return tok, nil
	}
}

// AppendJSONString appends s as a JSON string the way json.dumps writes it: only the quote, the backslash
// and the control characters are escaped, plus every non-ASCII code point as \uXXXX when ensureASCII.
func AppendJSONString(dst []byte, s string, ensureASCII bool) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	for _, r := range s {
		switch {
		case r == '"':
			dst = append(dst, '\\', '"')
		case r == '\\':
			dst = append(dst, '\\', '\\')
		case r == '\n':
			dst = append(dst, '\\', 'n')
		case r == '\r':
			dst = append(dst, '\\', 'r')
		case r == '\t':
			dst = append(dst, '\\', 't')
		case r == '\b':
			dst = append(dst, '\\', 'b')
		case r == '\f':
			dst = append(dst, '\\', 'f')
		case r < 0x20:
			dst = append(dst, '\\', 'u', '0', '0', hex[r>>4], hex[r&0xf])
		case r < 0x7f || !ensureASCII:
			dst = utf8.AppendRune(dst, r)
		case r > 0xffff:
			r -= 0x10000
			dst = appendU(dst, 0xd800+(r>>10))
			dst = appendU(dst, 0xdc00+(r&0x3ff))
		default:
			dst = appendU(dst, r)
		}
	}
	return append(dst, '"')
}

func appendU(dst []byte, r rune) []byte {
	const hex = "0123456789abcdef"
	return append(dst, '\\', 'u', hex[r>>12&0xf], hex[r>>8&0xf], hex[r>>4&0xf], hex[r&0xf])
}
