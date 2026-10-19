package tpl

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/jeiang/atrium/internal/web/tpl/tpltest"
)

const strikethroughWhy = "markdown-it renders ~~x~~ as <s>; goldmark's Strikethrough extension renders <del>, which the sanitizer also keeps"

var corpusRewrites = []tpltest.Rewrite{
	{From: "<s>", To: "<del>", Why: strikethroughWhy},
	{From: "</s>", To: "</del>", Why: strikethroughWhy},
}

func TestMarkdownMatchesThePythonCorpus(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "..", "testdata", "corpora", "markup.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	lines := bufio.NewScanner(f)
	lines.Buffer(make([]byte, 0, 1<<16), 1<<20)
	records := 0
	for lines.Scan() {
		var rec struct {
			Name  string            `json:"name"`
			Text  string            `json:"text"`
			Pages map[string]string `json:"pages"`
			HTML  string            `json:"html"`
		}
		if err := json.Unmarshal(lines.Bytes(), &rec); err != nil {
			t.Fatal(err)
		}
		records++
		t.Run(rec.Name, func(t *testing.T) {
			tpltest.Compare(t, rec.HTML, string(MarkdownWith(rec.Text, rec.Pages)), corpusRewrites...)
		})
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if records == 0 {
		t.Fatal("empty corpus")
	}
}
