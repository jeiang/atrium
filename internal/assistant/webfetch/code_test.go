package webfetch

import (
	"strings"
	"testing"
)

func TestCodeBlocks(t *testing.T) {
	intro := strings.Repeat("Some intro text here. ", 12)
	m := "<html><body><article><p>" + intro + "</p><pre><code>line one\n  indented two\nline three</code></pre><p>Use <code>foo()</code> inline now.</p></article></body></html>"
	_, text := ExtractText(m, "https://e.test/")
	want := strings.TrimSpace(intro) + "\nline one\n  indented two\nline three\nUse foo() inline now."
	if text != want {
		t.Errorf("text %q, want %q", text, want)
	}
}
