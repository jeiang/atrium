package render

import (
	"crypto/sha256"
	"encoding/hex"
	"html"
	"regexp"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/tool"
)

type Source struct {
	ThreadTitle string
	PublicURL   string
	Date        string
}

type Page struct {
	Title    string
	Markdown string
	Hash     string
}

type Entry struct {
	Cite    string
	Title   string
	Scope   string
	Writer  string
	Updated string
	Link    string
}

var (
	imagePattern = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	embedPattern = regexp.MustCompile(`!\[\[([^\]]*)\]\]`)
	urlPattern   = regexp.MustCompile(`(?i)https?://[^\s<>()\[\]]+`)
)

var markdownEscaper = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	`*`, `\*`,
	`_`, `\_`,
	`[`, `\[`,
	`]`, `\]`,
	`(`, `\(`,
	`)`, `\)`,
	`#`, `\#`,
	`!`, `\!`,
	`|`, `\|`,
	`~`, `\~`,
	`{`, `\{`,
	`}`, `\}`,
)

func defang(url string) string {
	lower := strings.ToLower(url)
	switch {
	case strings.HasPrefix(lower, "https://"):
		url = "hxxps://" + url[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		url = "hxxp://" + url[len("http://"):]
	}
	return strings.ReplaceAll(url, ".", "[.]")
}

func plain(text string) string {
	text = imagePattern.ReplaceAllStringFunc(text, func(match string) string {
		alt := imagePattern.FindStringSubmatch(match)[1]
		return "[image: " + markdownEscaper.Replace(html.EscapeString(alt)) + "]"
	})
	text = embedPattern.ReplaceAllStringFunc(text, func(match string) string {
		name := embedPattern.FindStringSubmatch(match)[1]
		return "[image: " + markdownEscaper.Replace(html.EscapeString(name)) + "]"
	})
	text = urlPattern.ReplaceAllStringFunc(text, defang)
	text = html.EscapeString(text)
	return markdownEscaper.Replace(text)
}

func title(r kb.Record) string {
	text := strings.Join(strings.Fields(r.Title), " ")
	if r.Marks&tool.Untrusted != 0 {
		return urlPattern.ReplaceAllStringFunc(text, defang)
	}
	return text
}

func linkText(text string) string {
	return strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(text)
}

func footnote(r kb.Record, src Source) string {
	parts := make([]string, 0, 5)
	switch {
	case r.SourceThreadID != nil:
		label := "thread " + strconv.FormatInt(*r.SourceThreadID, 10)
		if src.ThreadTitle != "" {
			label = src.ThreadTitle
		}
		source := label
		if src.PublicURL != "" {
			source = "[" + linkText(label) + "](" + src.PublicURL + "/chat/" + strconv.FormatInt(*r.SourceThreadID, 10)
			if r.SourceSeq != nil {
				source += "#m" + strconv.FormatInt(*r.SourceSeq, 10)
			}
			source += ")"
		}
		parts = append(parts, "Source: "+source)
	case r.Origin != nil:
		parts = append(parts, "Source: "+*r.Origin)
	default:
		parts = append(parts, "Source: operator")
	}
	parts = append(parts, "writer "+r.Writer)
	if r.SourceRunID != nil {
		parts = append(parts, "run "+strconv.FormatInt(*r.SourceRunID, 10))
	}
	if src.Date != "" {
		parts = append(parts, src.Date)
	}
	if !r.Confirmed() {
		parts = append(parts, "unconfirmed")
	}
	return "---\n" + strings.Join(parts, " · ")
}

func hash(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func RecordPage(r kb.Record, src Source) Page {
	body := r.Body
	if r.Marks&tool.Untrusted != 0 {
		body = plain(body)
	}
	markdown := strings.TrimRight(body, "\n") + "\n\n" + footnote(r, src) + "\n"
	name := title(r)
	return Page{Title: name, Markdown: markdown, Hash: hash(name, markdown)}
}

func Index(entries []Entry) Page {
	var b strings.Builder
	b.WriteString("Knowledge\n")
	for _, scope := range []string{kb.ScopeWork, kb.ScopePersonal} {
		var lines []string
		for _, e := range entries {
			if e.Scope != scope {
				continue
			}
			name := html.EscapeString(strings.Join(strings.Fields(e.Title), " "))
			line := "- " + e.Cite + " " + name
			if e.Link != "" {
				line = "- [" + e.Cite + " " + linkText(name) + "](" + e.Link + ")"
			}
			if e.Writer != "" {
				line += " · " + e.Writer
			}
			if e.Updated != "" {
				line += " · " + e.Updated
			}
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			continue
		}
		b.WriteString("\n## " + strings.ToUpper(scope[:1]) + scope[1:] + "\n")
		for _, line := range lines {
			b.WriteString(line + "\n")
		}
	}
	markdown := b.String()
	return Page{Title: "Knowledge", Markdown: markdown, Hash: hash("Knowledge", markdown)}
}
