package tpltest

import (
	"slices"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/py"
)

func TestCanonicalRules(t *testing.T) {
	doc := "<!DOCTYPE HTML>\n" +
		"<!-- dropped -->\n" +
		"<div class=\" a   b \" data-x=\"&#34;q&#34; &amp; &lt;\" hidden>  hello   &amp;\n" +
		"  world <br/> <input disabled></div>\n" +
		"<pre>\n keep   this\n</pre>\n" +
		"<p class=\"whitespace-pre-wrap\">  a   b <b> c </b></p>\n" +
		"<textarea>\n t  x</textarea>\n" +
		"<i id=\"cond-123456\">cond-654321</i>"
	want := []string{
		"<!doctype html>",
		`<div class="a b" data-x=""q" & <" hidden>`,
		`text "hello & world"`,
		"<br>",
		"<input disabled>",
		"</div>",
		"<pre>",
		`text " keep   this\n"`,
		"</pre>",
		`<p class="whitespace-pre-wrap">`,
		`text "  a   b "`,
		"<b>",
		`text " c "`,
		"</b>",
		"</p>",
		"<textarea>",
		`text " t  x"`,
		"</textarea>",
		`<i id="cond-NNNNNN">`,
		`text "cond-NNNNNN"`,
		"</i>",
	}
	got := Canonical(doc)
	if !slices.Equal(got, want) {
		t.Errorf("Canonical =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestCanonicalDecodesReferences(t *testing.T) {
	a := Canonical(`<a title="&#x27;x&#39; &quot;" href="/?a=1&amp;b=2">&lt;&copy;</a>`)
	b := Canonical(`<a title="'x' &#34;" href="/?a=1&b=2"><©</a>`)
	if !slices.Equal(a, b) {
		t.Errorf("%v != %v", a, b)
	}
}

func TestCompareAndDiff(t *testing.T) {
	if got := Diff(Canonical("<p>a</p>"), Canonical("<p> a </p>\n")); got != "" {
		t.Errorf("whitespace-only difference reported: %s", got)
	}
	diff := Diff(Canonical("<p>a</p><p>b</p>"), Canonical("<p>a</p><p>c</p>"))
	if !strings.Contains(diff, `- text "b"`) || !strings.Contains(diff, `+ text "c"`) {
		t.Errorf("diff = %q", diff)
	}
	if Diff(Canonical("<p>a</p>"), Canonical("<p>a</p><p>b</p>")) == "" {
		t.Error("an extra element went unreported")
	}
	rec := &recorder{}
	Compare(rec, `<a href="x" rel="noopener noreferrer">y</a><b rel="noopener noreferrer">z</b>`, `<a href="x" rel="noreferrer">y</a><b rel="noopener noreferrer">z</b>`,
		Rewrite{From: `rel="noopener noreferrer"`, To: `rel="noreferrer"`, Only: func(token string) bool { return strings.HasPrefix(token, "<a ") }})
	if rec.failed {
		t.Errorf("documented rewrite not applied: %s", rec.msg)
	}
	rec = &recorder{}
	Compare(rec, "<p>a</p>", "<p>b</p>")
	if !rec.failed {
		t.Error("different documents compared equal")
	}
}

type recorder struct {
	testing.TB
	failed bool
	msg    string
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failed = true
	r.msg = strings.TrimSpace(format)
}

func TestLoadKeepsKeyOrder(t *testing.T) {
	c := Load(t, "proposals.context.json")
	counts, ok := c.Context["counts"].(map[string]any)
	if !ok {
		t.Fatalf("counts is %T", c.Context["counts"])
	}
	var keys []string
	for _, p := range py.Items(counts) {
		keys = append(keys, p.Key)
	}
	if want := []string{"pending", "applied", "failed"}; !slices.Equal(keys, want) {
		t.Errorf("counts order = %v, want %v", keys, want)
	}
}
