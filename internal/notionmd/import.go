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
	mdLinkRe      = regexp.MustCompile(`!?\[([^\]]*)\]\(\s*<?([^)\s>]+)>?(?:\s+"[^"]*")?\s*\)`)
	refDefRe      = regexp.MustCompile(`(?im)^[ \t]{0,3}\[([^\]\n]+)\]:[ \t]*<?((?:https?://|notion://)[^\s>]+)>?[^\n]*(?:\n|$)`)
	refStyleRe    = regexp.MustCompile(`!?\[([^\]\n]+)\](?:\[([^\]\n]*)\])?`)
	autoURLRe     = regexp.MustCompile(`(?i)<((?:https?://|notion://)[^\s<>]+)>`)
	bareURLRe     = regexp.MustCompile("(?i)(?:https?://|notion://)[^\\s<>()\\[\\]\"`]+|\\b(?:[a-z0-9-]+\\.)*(?:notion\\.so|notion\\.com|notion\\.site)(?:[/?#][^\\s<>()\\[\\]\"`]*)?")
	tagStripRe    = regexp.MustCompile(`</?[A-Za-z][\w-]*(?:\s[^<>]*)?/?>`)
	tagStartRe    = regexp.MustCompile(`<(/?[A-Za-z!?])`)
	entityRe      = regexp.MustCompile(`&(?:#[0-9]+|#[xX][0-9a-fA-F]+|[A-Za-z][A-Za-z0-9]*);`)
)

const (
	ltSentinel = "\x00lt\x00"
	gtSentinel = "\x00gt\x00"
)

func Import(s string, refs, titles map[string]string) string {
	definitions := map[string]string{}
	for i, piece := range splitKeep(codeRe, s) {
		if i%2 == 1 {
			continue
		}
		for _, loc := range refDefRe.FindAllStringSubmatchIndex(piece, -1) {
			if raw := group(piece, loc, 2); notionURL(raw) {
				definitions[refName(group(piece, loc, 1))] = raw
			}
		}
	}
	return importConvert(s, refs, titles, definitions, true)
}

func refName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

func importConvert(s string, refs, titles, definitions map[string]string, decode bool) string {
	pieces := splitKeep(codeRe, s)
	var b strings.Builder
	for i, piece := range pieces {
		if i%2 == 1 {
			b.WriteString(piece)
			continue
		}
		b.WriteString(importPart(piece, refs, titles, definitions, decode))
	}
	return b.String()
}

func importPart(chunk string, refs, titles, definitions map[string]string, decode bool) string {
	if decode {
		chunk = protectAngles(chunk)
		chunk = html.UnescapeString(chunk)
		chunk = strings.ReplaceAll(chunk, "\u00a0", " ")
	}
	chunk = sub(calloutRe, chunk, true, func(loc []int) string {
		return quote(importConvert(group(chunk, loc, 1), refs, titles, definitions, false))
	})
	chunk = subText(calloutSelfRe, "", chunk)
	chunk = sub(detailsRe, chunk, true, func(loc []int) string {
		title := strings.TrimSpace(group(chunk, loc, 1))
		body := strings.TrimSpace(importConvert(group(chunk, loc, 2), refs, titles, definitions, false))
		return "**" + title + "**\n\n" + body + "\n\n"
	})
	chunk = sub(refRe, chunk, true, func(loc []int) string { return importRef(chunk, loc, refs) })
	chunk = sub(refDefRe, chunk, true, func(loc []int) string {
		if notionURL(group(chunk, loc, 2)) {
			return ""
		}
		return chunk[loc[0]:loc[1]]
	})
	chunk = sub(mdLinkRe, chunk, true, func(loc []int) string { return importLink(chunk, loc, refs) })
	chunk = sub(refStyleRe, chunk, true, func(loc []int) string {
		label := group(chunk, loc, 1)
		name := group(chunk, loc, 2)
		if name == "" {
			name = label
		}
		raw, ok := definitions[refName(name)]
		if !ok {
			return chunk[loc[0]:loc[1]]
		}
		if replacement, ok := refReplacement(raw, refs); ok {
			return replacement
		}
		return label
	})
	chunk = sub(autoURLRe, chunk, true, func(loc []int) string {
		raw := group(chunk, loc, 1)
		if !notionURL(raw) {
			return chunk[loc[0]:loc[1]]
		}
		return bareReplacement(raw, refs, titles)
	})
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
	chunk = sub(bareURLRe, chunk, true, func(loc []int) string {
		raw := chunk[loc[0]:loc[1]]
		if loc[0] > 0 && chunk[loc[0]-1] == '@' {
			return raw
		}
		trimmed := strings.TrimRight(raw, ".,;:!?")
		if !notionURL(trimmed) {
			return raw
		}
		return bareReplacement(trimmed, refs, titles) + raw[len(trimmed):]
	})
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

func bareReplacement(raw string, refs, titles map[string]string) string {
	if replacement, ok := refReplacement(raw, refs); ok {
		return replacement
	}
	for _, id := range notionid.IDsInText(raw) {
		if title := strings.TrimSpace(titles[id]); title != "" {
			return title
		}
	}
	return ""
}

func notionURL(raw string) bool {
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "notion://") {
		return true
	}
	if !strings.Contains(lower, "://") {
		raw = "https://" + raw
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
