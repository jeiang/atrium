package notionmd

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/notionid"
)

var (
	calloutRe     = regexp.MustCompile(`(?s)<callout\b[^>]*>(.*?)</callout>`)
	calloutSelfRe = regexp.MustCompile(`<callout\b[^>]*/>`)
	detailsRe     = regexp.MustCompile(`(?s)<details\b[^>]*>\s*<summary>(.*?)</summary>(.*?)</details>`)
	columnRe      = regexp.MustCompile(`(?s)<column\b[^>]*>(.*?)</column>`)
	mdLinkRe      = regexp.MustCompile(`\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	tagStripRe    = regexp.MustCompile(`</?[A-Za-z][\w-]*(?:\s[^<>]*)?/?>`)
	tagStartRe    = regexp.MustCompile(`<(/?[A-Za-z!?])`)
	entityRe      = regexp.MustCompile(`&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);`)
)

const (
	ltSentinel = "\x00lt\x00"
	gtSentinel = "\x00gt\x00"
)

func Import(s string, refs map[string]string) string {
	return importConvert(s, refs, true)
}

func importConvert(s string, refs map[string]string, decode bool) string {
	pieces := splitKeep(codeRe, s)
	var b strings.Builder
	for i, piece := range pieces {
		if i%2 == 1 {
			b.WriteString(piece)
			continue
		}
		b.WriteString(importPart(piece, refs, decode))
	}
	return b.String()
}

func importPart(chunk string, refs map[string]string, decode bool) string {
	if decode {
		chunk = protectAngles(chunk)
		chunk = html.UnescapeString(chunk)
		chunk = strings.ReplaceAll(chunk, "\u00a0", " ")
	}
	chunk = sub(calloutRe, chunk, true, func(loc []int) string {
		return quote(importConvert(group(chunk, loc, 1), refs, false))
	})
	chunk = subText(calloutSelfRe, "", chunk)
	chunk = sub(detailsRe, chunk, true, func(loc []int) string {
		title := strings.TrimSpace(group(chunk, loc, 1))
		body := strings.TrimSpace(importConvert(group(chunk, loc, 2), refs, false))
		return "**" + title + "**\n\n" + body + "\n\n"
	})
	chunk = sub(refRe, chunk, true, func(loc []int) string { return importRef(chunk, loc, refs) })
	chunk = sub(mdLinkRe, chunk, true, func(loc []int) string { return importLink(chunk, loc, refs) })
	chunk = sub(tableRe, chunk, true, func(loc []int) string { return tableMarkdown(chunk, loc) })
	chunk = sub(mediaRe, chunk, true, func(loc []int) string { return strings.TrimSpace(group(chunk, loc, 3)) })
	chunk = sub(columnRe, chunk, true, func(loc []int) string {
		return "\n\n" + strings.TrimSpace(group(chunk, loc, 1)) + "\n\n"
	})
	chunk = sub(dateRe, chunk, true, func(loc []int) string { return dateText(chunk, loc) })
	chunk = subText(emptyRe, "", chunk)
	chunk = sub(wrapper, chunk, true, func([]int) string { return "" })
	chunk = sub(breakRe, chunk, true, func([]int) string { return "\n" })
	chunk = subText(blockAtt, "", chunk)
	chunk = sub(tagStripRe, chunk, true, func([]int) string { return "" })
	chunk = restoreAngles(chunk)
	chunk = sub(tagStartRe, chunk, false, func(loc []int) string {
		rest := strings.ToLower(chunk[loc[1]:])
		if strings.HasPrefix(rest, "ttp://") || strings.HasPrefix(rest, "ttps://") || strings.HasPrefix(rest, "ailto:") {
			return chunk[loc[0]:loc[1]]
		}
		return "&lt;" + group(chunk, loc, 1)
	})
	return chunk
}

func protectAngles(s string) string {
	return sub(entityRe, s, false, func(loc []int) string {
		ent := s[loc[0]:loc[1]]
		switch html.UnescapeString(ent) {
		case "<":
			return ltSentinel
		case ">":
			return gtSentinel
		default:
			return ent
		}
	})
}

func restoreAngles(s string) string {
	s = strings.ReplaceAll(s, ltSentinel, "&lt;")
	return strings.ReplaceAll(s, gtSentinel, "&gt;")
}

func quote(text string) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			lines[i] = ">"
			continue
		}
		lines[i] = "> " + line
	}
	return strings.Join(lines, "\n") + "\n"
}

func importRef(s string, loc []int, refs map[string]string) string {
	a := attrs(group(s, loc, 2))
	url := a["url"]
	if url == "" {
		url = a["data-source-url"]
	}
	if replacement, ok := refReplacement(url, refs); ok {
		return replacement
	}
	title := strings.TrimSpace(group(s, loc, 3))
	if title == "" {
		title = "Untitled"
	}
	return title
}

func importLink(s string, loc []int, refs map[string]string) string {
	if loc[0] > 0 && s[loc[0]-1] == '!' {
		return s[loc[0]:loc[1]]
	}
	label := group(s, loc, 1)
	raw := group(s, loc, 2)
	if !notionURL(raw) {
		return s[loc[0]:loc[1]]
	}
	if replacement, ok := refReplacement(raw, refs); ok {
		return replacement
	}
	return label
}

func refReplacement(raw string, refs map[string]string) (string, bool) {
	for _, id := range notionid.IDsInText(raw) {
		if replacement, ok := refs[id]; ok {
			return replacement, true
		}
	}
	return "", false
}

func notionURL(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "notion://") {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{"notion.so", "notion.com", "notion.site"} {
		if host == suffix || strings.HasSuffix(host, "."+suffix) {
			return true
		}
	}
	return false
}
