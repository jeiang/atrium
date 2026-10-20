package golden

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
)

const maxDiffs = 20

type Request struct {
	Method       string            `json:"method"`
	URL          string            `json:"url"`
	Path         string            `json:"path"`
	Query        string            `json:"query"`
	Headers      map[string]string `json:"headers"`
	BodyEncoding string            `json:"body_encoding"`
	Body         json.RawMessage   `json:"body"`
}

type Exchange struct {
	Request  Request         `json:"request"`
	Response json.RawMessage `json:"response"`
}

type Scenario struct {
	Name         string            `json:"scenario"`
	Description  string            `json:"description"`
	SourceTests  []string          `json:"source_tests"`
	FreezeCommit string            `json:"freeze_commit"`
	Exchanges    []Exchange        `json:"exchanges"`
	Runs         []json.RawMessage `json:"runs"`
	Usage        []json.RawMessage `json:"usage"`
}

func Dir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("golden: cannot locate source file")
	}
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "testdata", "golden")
}

func ReadFile(t testing.TB, slice, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(Dir(), slice, name))
	if err != nil {
		t.Fatalf("golden: %v", err)
	}
	return data
}

func LoadScenario(t testing.TB, slice, name string) Scenario {
	t.Helper()
	var s Scenario
	dec := json.NewDecoder(bytes.NewReader(ReadFile(t, slice, name+".json")))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("golden: %s/%s.json: %v", slice, name, err)
	}
	if s.Name != name {
		t.Fatalf("golden: %s/%s.json names scenario %q", slice, name, s.Name)
	}
	return s
}

func Diff(want, got []byte) ([]string, error) {
	w, err := decode(want)
	if err != nil {
		return nil, fmt.Errorf("want: %w", err)
	}
	g, err := decode(got)
	if err != nil {
		return nil, fmt.Errorf("got: %w", err)
	}
	var diffs []string
	compare("$", w, g, &diffs)
	return diffs, nil
}

func decode(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after JSON value")
	}
	return normalize(v)
}

func normalize(v any) (any, error) {
	switch x := v.(type) {
	case json.Number:
		return number(x)
	case []any:
		for i, e := range x {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			x[i] = n
		}
	case map[string]any:
		for k, e := range x {
			n, err := normalize(e)
			if err != nil {
				return nil, err
			}
			x[k] = n
		}
	}
	return v, nil
}

type num string

func number(n json.Number) (num, error) {
	r, ok := new(big.Rat).SetString(string(n))
	if !ok {
		return "", fmt.Errorf("invalid number %q", n)
	}
	if r.IsInt() {
		return num(r.Num().String()), nil
	}
	f, _ := r.Float64()
	if math.IsInf(f, 0) {
		return "", fmt.Errorf("number %q overflows float64", n)
	}
	return num(strconv.FormatFloat(f, 'g', -1, 64)), nil
}

func encode(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		buf.WriteString(strconv.FormatBool(x))
	case num:
		buf.WriteString(string(x))
	case string:
		return encodeString(buf, x)
	case []any:
		buf.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encode(buf, e); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		buf.WriteByte('{')
		for i, k := range slices.Sorted(maps.Keys(x)) {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := encodeString(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := encode(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("unexpected JSON value %T", v)
	}
	return nil
}

func encodeString(buf *bytes.Buffer, s string) error {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return err
	}
	buf.Truncate(buf.Len() - 1)
	return nil
}

func compare(path string, want, got any, diffs *[]string) {
	if len(*diffs) >= maxDiffs {
		return
	}
	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			mismatch(path, want, got, diffs)
			return
		}
		keys := slices.Sorted(maps.Keys(w))
		for _, k := range keys {
			sub := path + "." + k
			gv, present := g[k]
			if !present {
				addDiff(diffs, "%s: missing, want %s", sub, short(w[k]))
				continue
			}
			compare(sub, w[k], gv, diffs)
		}
		for _, k := range slices.Sorted(maps.Keys(g)) {
			if _, present := w[k]; !present {
				addDiff(diffs, "%s.%s: unexpected %s", path, k, short(g[k]))
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			mismatch(path, want, got, diffs)
			return
		}
		for i := range min(len(w), len(g)) {
			compare(fmt.Sprintf("%s[%d]", path, i), w[i], g[i], diffs)
		}
		if len(w) != len(g) {
			addDiff(diffs, "%s: length %d, want %d", path, len(g), len(w))
		}
	default:
		if want != got {
			mismatch(path, want, got, diffs)
		}
	}
}

func mismatch(path string, want, got any, diffs *[]string) {
	addDiff(diffs, "%s: got %s, want %s", path, short(got), short(want))
}

func addDiff(diffs *[]string, format string, args ...any) {
	if len(*diffs) < maxDiffs {
		*diffs = append(*diffs, fmt.Sprintf(format, args...))
	}
}

func short(v any) string {
	var buf bytes.Buffer
	if err := encode(&buf, v); err != nil {
		return fmt.Sprintf("%v", v)
	}
	if s := buf.String(); len(s) <= 120 {
		return s
	}
	return buf.String()[:117] + "..."
}
