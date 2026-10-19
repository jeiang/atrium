package tpl

import (
	"strings"
	"testing"
)

func TestMarkdownRendersCommonmarkSubset(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"emphasis", "**b** *i* `c` ~~s~~", "<p><strong>b</strong> <em>i</em> <code>c</code> <del>s</del></p>\n"},
		{"hard break", "a\nb", "<p>a<br>\nb</p>\n"},
		{"lists", "- a\n- b\n\n1. x\n2. y", "<ul>\n<li>a</li>\n<li>b</li>\n</ul>\n<ol>\n<li>x</li>\n<li>y</li>\n</ol>\n"},
		{"fence keeps language", "```py\nprint('<hi>')\n```", "<pre><code class=\"language-py\">print(&#39;&lt;hi&gt;&#39;)\n</code></pre>\n"},
		{"table", "| a | b |\n|---|---|\n| 1 | 2 |", "<table>\n<thead>\n<tr>\n<th>a</th>\n<th>b</th>\n</tr>\n</thead>\n<tbody>\n<tr>\n<td>1</td>\n<td>2</td>\n</tr>\n</tbody>\n</table>\n"},
		{"heading and rule", "# h\n\n---", "<h1>h</h1>\n<hr>\n"},
		{"quote", "> q", "<blockquote>\n<p>q</p>\n</blockquote>\n"},
		{"empty", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(Markdown(tc.in)); got != tc.want {
				t.Errorf("Markdown(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownIndentedTextIsNotCode(t *testing.T) {
	if got, want := string(Markdown("    four spaces")), "<p>four spaces</p>\n"; got != want {
		t.Errorf("Markdown = %q, want %q", got, want)
	}
}

func TestMarkdownRawHTMLIsEscapedText(t *testing.T) {
	for in, want := range map[string]string{
		`<script>alert("xss")</script>`: "<p>&lt;script&gt;alert(&#34;xss&#34;)&lt;/script&gt;</p>\n",
		`a <b>bold</b> c`:               "<p>a &lt;b&gt;bold&lt;/b&gt; c</p>\n",
		"<div>\nx\n</div>":              "<p>&lt;div&gt;<br>\nx<br>\n&lt;/div&gt;</p>\n",
	} {
		if got := string(Markdown(in)); got != want {
			t.Errorf("Markdown(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMarkdownStripsUnsafeLinksAndImages(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"javascript link", "[bad](javascript:alert(1))", "<p>bad</p>\n"},
		{"data link", "[bad](data:text/html;base64,PHNjcmlwdD4=)", "<p>bad</p>\n"},
		{"vbscript link", "[bad](vbscript:x)", "<p>bad</p>\n"},
		{"relative link", "[home](/chat)", "<p>home</p>\n"},
		{"mailto link", "[me](mailto:a@b.c)", "<p>me</p>\n"},
		{"image", "![alt](https://e.com/x.png)", `<p><a href="https://e.com/x.png" rel="noreferrer">alt</a></p>` + "\n"},
		{"onerror image", `<img src=x onerror=alert(1)>`, "<p>&lt;img src=x onerror=alert(1)&gt;</p>\n"},
		{"https link", "[ok](https://e.com/a?b=1&c=2)", `<p><a href="https://e.com/a?b=1&amp;c=2" rel="noreferrer">ok</a></p>` + "\n"},
		{"notion link", "[n](notion://x)", `<p><a href="notion://x" rel="noreferrer">n</a></p>` + "\n"},
		{"title survives", `[ok](https://e.com "t")`, `<p><a href="https://e.com" title="t" rel="noreferrer">ok</a></p>` + "\n"},
		{"autolink", "<https://e.com>", `<p><a href="https://e.com" rel="noreferrer">https://e.com</a></p>` + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := string(Markdown(tc.in)); got != tc.want {
				t.Errorf("Markdown(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestMarkdownNeverLeavesActiveContent(t *testing.T) {
	for _, in := range []string{
		`<script>alert(1)</script>`, `[x](javascript:alert(1))`, `![x](javascript:alert(1))`, `<a href="javascript:alert(1)">x</a>`,
		`<iframe src=x></iframe>`, `<svg onload=alert(1)>`, "[x](  javascript:alert(1))", "[x](JaVaScRiPt:alert(1))",
		"[x](java\tscript:alert(1))", `<style>*{}</style>`, "```\n<script>\n```", "<img src=x onerror=alert(1)>",
		"[x](https://e.com\" onmouseover=\"alert(1))", "[a](<javascript:alert(1)>)", "[x]: javascript:alert(1)\n\n[x]",
	} {
		out := strings.ToLower(string(Markdown(in)))
		for _, tag := range []string{"<script", "<img", "<iframe", "<svg", "<style", "<a href=\"javascript", "\" on"} {
			if strings.Contains(out, tag) {
				t.Errorf("Markdown(%q) = %q contains %q", in, out, tag)
			}
		}
	}
}
