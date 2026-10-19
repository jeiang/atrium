package pyhtml

import (
	"bufio"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type parserCase struct {
	Input     string  `json:"input"`
	Scripting bool    `json:"scripting"`
	Events    [][]any `json:"events"`
}

func record(c parserCase) [][]any {
	got := [][]any{}
	attrs := func(attrs []Attr) []any {
		out := []any{}
		for _, a := range attrs {
			var v any
			if a.Value != nil {
				v = *a.Value
			}
			out = append(out, []any{a.Name, v})
		}
		return out
	}
	p := &Parser{
		Scripting:   c.Scripting,
		StartTag:    func(tag string, a []Attr) { got = append(got, []any{"start", tag, attrs(a)}) },
		StartEndTag: func(tag string, a []Attr) { got = append(got, []any{"startend", tag, attrs(a)}) },
		EndTag:      func(tag string) { got = append(got, []any{"end", tag}) },
		Data:        func(d string) { got = append(got, []any{"data", d}) },
		Comment:     func(d string) { got = append(got, []any{"comment", d}) },
		Decl:        func(d string) { got = append(got, []any{"decl", d}) },
		PI:          func(d string) { got = append(got, []any{"pi", d}) },
		UnknownDecl: func(d string) { got = append(got, []any{"unknown_decl", d}) },
	}
	p.Feed(c.Input)
	p.Close()
	return got
}

func TestParserMatchesCPython(t *testing.T) {
	f, err := os.Open("testdata/parser.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for sc.Scan() {
		var c parserCase
		if err := json.Unmarshal(sc.Bytes(), &c); err != nil {
			t.Fatal(err)
		}
		if c.Events == nil {
			c.Events = [][]any{}
		}
		if got := record(c); !reflect.DeepEqual(got, c.Events) {
			t.Errorf("%q (scripting %v):\n got %v\nwant %v", c.Input, c.Scripting, got, c.Events)
		}
		n++
	}
	if n < 1000 {
		t.Fatalf("only %d cases", n)
	}
}

func TestParserFeedInPieces(t *testing.T) {
	var data []string
	p := &Parser{Data: func(s string) { data = append(data, s) }}
	p.Feed("a &am")
	p.Feed("p; b <")
	p.Feed("b>c")
	p.Close()
	joined := ""
	for _, s := range data {
		joined += s
	}
	if joined != "a & b c" {
		t.Errorf("joined data %q", joined)
	}
}
