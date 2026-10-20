package loop

import (
	"encoding/json"
	"testing"

	"github.com/jeiang/atrium/internal/py"
)

func TestJournalBlockIsPythonModelDump(t *testing.T) {
	cases := []struct{ wire, want string }{
		{`{"type":"tool_use","id":"t1","name":"probe","input":{"z":1,"a":2}}`, `{"id":"t1","input":{"z":1,"a":2},"name":"probe","type":"tool_use"}`},
		{`{"type":"thinking","thinking":"deep","signature":"s"}`, `{"signature":"s","thinking":"deep","type":"thinking"}`},
		{`{"type":"text","text":"hi","citations":[{"type":"char_location","document_title":"a.csv","cited_text":"x","start_char_index":0,"end_char_index":1,"document_index":0}]}`,
			`{"citations":[{"cited_text":"x","document_index":0,"document_title":"a.csv","end_char_index":1,"start_char_index":0,"type":"char_location"}],"text":"hi","type":"text"}`},
		{`{"type":"web_search_tool_result","tool_use_id":"s1","content":[{"type":"web_search_result","url":"u","title":"T","encrypted_content":"x","page_age":null}]}`,
			`{"content":[{"encrypted_content":"x","page_age":null,"title":"T","type":"web_search_result","url":"u"}],"tool_use_id":"s1","type":"web_search_tool_result"}`},
		{`{"type":"web_search_tool_result","tool_use_id":"s1","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}}`,
			`{"content":{"error_code":"max_uses_exceeded","type":"web_search_tool_result_error"},"tool_use_id":"s1","type":"web_search_tool_result"}`},
		{`{"type":"server_tool_use","name":"web_search","id":"s1","input":{"query":"q"},"caller":{"type":"direct"}}`,
			`{"id":"s1","input":{"query":"q"},"name":"web_search","type":"server_tool_use","caller":{"type":"direct"}}`},
		{`{"type":"compaction","signature":"g","content":"sum","encrypted_content":"e"}`, `{"content":"sum","encrypted_content":"e","type":"compaction","signature":"g"}`},
		{`{"zeta":1,"type":"text","text":"x","alpha":2}`, `{"text":"x","type":"text","zeta":1,"alpha":2}`},
		{`{"type":"brand_new","b":1,"a":2}`, `{"type":"brand_new","b":1,"a":2}`},
	}
	for _, c := range cases {
		got, err := journalBlock(json.RawMessage(c.wire))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != c.want {
			t.Errorf("%s\n got %s\nwant %s", c.wire, got, c.want)
		}
	}
}

func TestArgsIsInputOrEmptyObject(t *testing.T) {
	cases := []struct {
		input    string
		journal  string
		isObject bool
	}{
		{``, `{}`, true},
		{`null`, `{}`, true},
		{`[]`, `{}`, true},
		{`0`, `{}`, true},
		{`{"a":1}`, `{"a":1}`, true},
		{`[1]`, `[1]`, false},
		{`"x"`, `"x"`, false},
	}
	for _, c := range cases {
		raw, value, err := ToolUse{Name: "t", Input: json.RawMessage(c.input)}.Args()
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := value.(py.Dict); string(raw) != c.journal || ok != c.isObject {
			t.Errorf("input %q: journal %s object %v", c.input, raw, ok)
		}
	}
}
