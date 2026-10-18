package py

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// FloatRepr is repr(float).
func FloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	exp := strconv.FormatFloat(f, 'e', -1, 64)
	at := strings.IndexByte(exp, 'e')
	e, _ := strconv.Atoi(exp[at+1:])
	if e < -4 || e >= 16 {
		return exp
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// ReprValue is repr of a decoded JSON value: nil, bool, int, int64, *big.Int, float64, string, []string, []any,
// Dict or map[string]any (in key order).
func ReprValue(v any) string {
	switch v := v.(type) {
	case nil:
		return "None"
	case bool:
		if v {
			return "True"
		}
		return "False"
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case *big.Int:
		return v.String()
	case float64:
		return FloatRepr(v)
	case string:
		return Repr(v)
	case []string:
		parts := make([]string, len(v))
		for i, s := range v {
			parts[i] = Repr(s)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case []any:
		parts := make([]string, len(v))
		for i, e := range v {
			parts[i] = ReprValue(e)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case Dict:
		parts := make([]string, len(v))
		for i, p := range v {
			parts[i] = Repr(p.Key) + ": " + ReprValue(p.Value)
		}
		return "{" + strings.Join(parts, ", ") + "}"
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		parts := make([]string, len(keys))
		for i, k := range keys {
			parts[i] = Repr(k) + ": " + ReprValue(v[k])
		}
		return "{" + strings.Join(parts, ", ") + "}"
	}
	panic(fmt.Sprintf("py: ReprValue of %T", v))
}

// TypeName is type(v).__name__ of a decoded JSON value.
func TypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case int, int64, *big.Int:
		return "int"
	case float64:
		return "float"
	case string:
		return "str"
	case []string, []any:
		return "list"
	}
	return "dict"
}

// Get is d.get(key): the value of the last pair named key.
func (d Dict) Get(key string) (any, bool) {
	for i := len(d) - 1; i >= 0; i-- {
		if d[i].Key == key {
			return d[i].Value, true
		}
	}
	return nil, false
}

// Set is d[key] = value: it replaces the pair in place or appends one.
func (d Dict) Set(key string, value any) Dict {
	out := slices.Clone(d)
	for i := range out {
		if out[i].Key == key {
			out[i].Value = value
			return out
		}
	}
	return append(out, Pair{key, value})
}

// JSONLoads is json.loads for what encoding/json accepts: objects become Dict (a duplicate key keeps its first
// position and its last value), arrays []any, integers int64 or *big.Int, other numbers float64.
func JSONLoads(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("Extra data")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok := tok.(type) {
	case json.Delim:
		if tok == '[' {
			out := []any{}
			for dec.More() {
				e, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, e)
			}
			_, err := dec.Token()
			return out, err
		}
		out := Dict{}
		for dec.More() {
			k, err := dec.Token()
			if err != nil {
				return nil, err
			}
			e, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			out = out.Set(k.(string), e)
		}
		_, err := dec.Token()
		return out, err
	case json.Number:
		return number(tok.String())
	}
	return tok, nil
}

func number(text string) (any, error) {
	if !strings.ContainsAny(text, ".eE") {
		if n, err := strconv.ParseInt(text, 10, 64); err == nil {
			return n, nil
		}
		n, ok := new(big.Int).SetString(text, 10)
		if !ok {
			return nil, fmt.Errorf("bad number %q", text)
		}
		return n, nil
	}
	f, err := strconv.ParseFloat(text, 64)
	if err != nil && !math.IsInf(f, 0) {
		return nil, err
	}
	return f, nil
}
