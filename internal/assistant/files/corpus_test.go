package files

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/rx"
	"github.com/jeiang/atrium/internal/rx/rxtest"
)

const corpusDir = "../../../testdata/corpora"

type corpusError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type corpusData struct {
	File      string `json:"file"`
	B64       string `json:"b64"`
	RepeatB64 string `json:"repeat_b64"`
	Count     int    `json:"count"`
}

type corpusRecord struct {
	Fn           string          `json:"fn"`
	Case         string          `json:"case"`
	Name         string          `json:"name"`
	SuppliedType *string         `json:"supplied_type"`
	Data         corpusData      `json:"data"`
	Input        []any           `json:"input"`
	Output       json.RawMessage `json:"output"`
	Error        *corpusError    `json:"error"`
}

type inspectOutput struct {
	Kind      string  `json:"kind"`
	MediaType string  `json:"media_type"`
	Text      *string `json:"text"`
	Tokens    int64   `json:"tokens"`
}

var textDivergences = map[string]string{}

func (d corpusData) bytes(t *testing.T) []byte {
	t.Helper()
	switch {
	case d.File != "":
		data, err := os.ReadFile(filepath.Join(corpusDir, d.File))
		if err != nil {
			t.Fatal(err)
		}
		return data
	case d.RepeatB64 != "":
		unit, err := base64.StdEncoding.DecodeString(d.RepeatB64)
		if err != nil {
			t.Fatal(err)
		}
		return bytes.Repeat(unit, d.Count)
	}
	data, err := base64.StdEncoding.DecodeString(d.B64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func readCorpus(t *testing.T) []corpusRecord {
	t.Helper()
	f, err := os.Open(filepath.Join(corpusDir, "documents.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []corpusRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 1<<24)
	for sc.Scan() {
		var r corpusRecord
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func errorHead(msg string) string {
	for _, prefix := range []string{"could not read the Word file: ", "could not read the Excel file: "} {
		if i := strings.Index(msg, prefix); i >= 0 {
			return msg[:i+len(prefix)]
		}
	}
	return msg
}

func (r corpusRecord) checkInspect(t *testing.T) {
	supplied := ""
	if r.SuppliedType != nil {
		supplied = *r.SuppliedType
	}
	info, err := Inspect(r.Name, r.Data.bytes(t), supplied)
	if r.Error != nil {
		if err == nil {
			t.Fatalf("Inspect succeeded; Python: %s", r.Error.Message)
		}
		if got, want := errorHead(err.Error()), errorHead(r.Error.Message); got != want {
			t.Errorf("error %q, Python %q", got, want)
		}
		if _, ok := err.(FileError); !ok {
			t.Errorf("error is %T, not FileError", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var want inspectOutput
	if err := json.Unmarshal(r.Output, &want); err != nil {
		t.Fatal(err)
	}
	if info.Kind != want.Kind || info.MediaType != want.MediaType {
		t.Errorf("kind %q type %q, Python %q %q", info.Kind, info.MediaType, want.Kind, want.MediaType)
	}
	if reason, ok := textDivergences[r.Case]; ok {
		t.Logf("documented divergence: %s", reason)
		return
	}
	if (info.Text == nil) != (want.Text == nil) || info.Text != nil && *info.Text != *want.Text {
		t.Errorf("text %v, Python %q", info.Text, deref(want.Text))
	}
	if info.Tokens != want.Tokens {
		t.Errorf("tokens %d, Python %d", info.Tokens, want.Tokens)
	}
}

func deref(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}

func (r corpusRecord) checkBudget(t *testing.T) {
	var items []nameTokens
	for _, raw := range r.Input[0].([]any) {
		pair := raw.([]any)
		items = append(items, nameTokens{pair[0].(string), int64(pair[1].(float64))})
	}
	err := checkBudget(items, int64(r.Input[1].(float64)))
	switch {
	case r.Error == nil && err != nil:
		t.Errorf("checkBudget: %v", err)
	case r.Error != nil && (err == nil || err.Error() != r.Error.Message):
		t.Errorf("checkBudget error %v, Python %q", err, r.Error.Message)
	}
}

func TestDocumentsCorpus(t *testing.T) {
	records := readCorpus(t)
	if len(records) == 0 {
		t.Fatal("empty corpus")
	}
	for i, r := range records {
		name := r.Case
		if name == "" {
			name = r.Fn
		}
		t.Run(strings.ReplaceAll(name, " ", "_")+"/"+r.Fn, func(t *testing.T) {
			_ = i
			switch r.Fn {
			case "inspect":
				r.checkInspect(t)
			case "safe_name":
				var want string
				_ = json.Unmarshal(r.Output, &want)
				var in string
				if s, ok := r.Input[0].(string); ok {
					in = s
				}
				if got := SafeName(in); got != want {
					t.Errorf("SafeName(%q) = %q, Python %q", in, got, want)
				}
			case "media_type_of":
				var want string
				_ = json.Unmarshal(r.Output, &want)
				name, _ := r.Input[0].(string)
				supplied, _ := r.Input[1].(string)
				if got := MediaTypeOf(name, supplied); got != want {
					t.Errorf("MediaTypeOf(%q, %q) = %q, Python %q", name, supplied, got, want)
				}
			case "kind_of":
				var want *string
				_ = json.Unmarshal(r.Output, &want)
				wantKind := ""
				if want != nil {
					wantKind = *want
				}
				if got := KindOf(r.Input[0].(string)); got != wantKind {
					t.Errorf("KindOf(%q) = %q, Python %q", r.Input[0], got, wantKind)
				}
			case "check_budget":
				r.checkBudget(t)
			default:
				t.Fatalf("unknown corpus function %q", r.Fn)
			}
		})
	}
}

func TestPatterns(t *testing.T) {
	rxtest.Run(t, map[string]*rx.Regexp{
		"src/atrium/agent/files.py:PAGE":               pageRe,
		"src/atrium/agent/files.py:safe_name:e8618e2d": nameRe,
	})
}
