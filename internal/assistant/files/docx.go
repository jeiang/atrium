package files

import (
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	nsWord         = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	relOfficeDoc   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"
	typeWordMain   = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	packageRels    = "_rels/.rels"
	packageRelBase = "/"
)

func contentTypeOf(types *element, partName string) (string, error) {
	for _, o := range types.kids(nsContentTypes, "Override") {
		if name, _ := o.attr("", "PartName"); name == partName {
			kind, _ := o.attr("", "ContentType")
			return kind, nil
		}
	}
	ext := strings.TrimPrefix(py.Lower(pathSuffix(pathName(partName))), ".")
	for _, d := range types.kids(nsContentTypes, "Default") {
		if e, _ := d.attr("", "Extension"); py.Lower(e) == ext {
			kind, _ := d.attr("", "ContentType")
			return kind, nil
		}
	}
	return "", errKey
}

func mainDocument(a *archive) (*element, error) {
	types, err := a.tree(contentTypes)
	if err != nil {
		return nil, err
	}
	var rels []relationship
	if a.has(packageRels) {
		root, err := a.tree(packageRels)
		if err != nil {
			return nil, err
		}
		rels = relationships(root)
	}
	var found []relationship
	for _, r := range rels {
		if !r.external && r.kind == relOfficeDoc {
			found = append(found, r)
		}
	}
	switch len(found) {
	case 0:
		return nil, errKey
	case 1:
	default:
		return nil, errValue
	}
	part := resolvePart(packageRelBase, found[0].target)
	kind, err := contentTypeOf(types, part)
	if err != nil {
		return nil, err
	}
	root, err := a.tree(strings.TrimPrefix(part, "/"))
	if err != nil {
		return nil, err
	}
	if kind != typeWordMain {
		return nil, errValue
	}
	return root, nil
}

func breakText(br *element) string {
	if kind, ok := br.attr(nsWord, "type"); ok && kind != "textWrapping" {
		return ""
	}
	return "\n"
}

func runText(r *element) (string, error) {
	var b strings.Builder
	for _, c := range r.children {
		if c.space != nsWord {
			continue
		}
		switch c.local {
		case "br":
			b.WriteString(breakText(c))
		case "cr":
			b.WriteByte('\n')
		case "noBreakHyphen":
			b.WriteByte('-')
		case "ptab", "tab":
			b.WriteByte('\t')
		case "t":
			b.WriteString(c.text)
		}
	}
	return b.String(), nil
}

func paragraphText(p *element) (string, error) {
	var b strings.Builder
	for _, c := range p.children {
		if c.space != nsWord {
			continue
		}
		switch c.local {
		case "r":
			s, err := runText(c)
			if err != nil {
				return "", err
			}
			b.WriteString(s)
		case "hyperlink":
			for _, r := range c.kids(nsWord, "r") {
				s, err := runText(r)
				if err != nil {
					return "", err
				}
				b.WriteString(s)
			}
		}
	}
	return b.String(), nil
}

func cellText(tc *element) (string, error) {
	var lines []string
	for _, p := range tc.kids(nsWord, "p") {
		s, err := paragraphText(p)
		if err != nil {
			return "", err
		}
		lines = append(lines, s)
	}
	return strings.Join(lines, "\n"), nil
}

func valueOf(parent *element, local string) (int, bool, error) {
	if parent == nil {
		return 0, false, nil
	}
	e := parent.kid(nsWord, local)
	if e == nil {
		return 0, false, nil
	}
	raw, ok := e.attr(nsWord, "val")
	if !ok {
		return 0, false, errValue
	}
	n, err := decimal(raw)
	return n, true, err
}

func gridSpan(tc *element) (int, error) {
	n, ok, err := valueOf(tc.kid(nsWord, "tcPr"), "gridSpan")
	if !ok {
		return 1, err
	}
	return n, err
}

func gridBefore(tr *element) (int, error) {
	n, _, err := valueOf(tr.kid(nsWord, "trPr"), "gridBefore")
	return n, err
}

func continuesMerge(tc *element) bool {
	tcPr := tc.kid(nsWord, "tcPr")
	if tcPr == nil {
		return false
	}
	merge := tcPr.kid(nsWord, "vMerge")
	if merge == nil {
		return false
	}
	val, ok := merge.attr(nsWord, "val")
	return !ok || val == "continue"
}

func gridOffset(tc *element) (int, error) {
	offset, err := gridBefore(tc.parent)
	if err != nil {
		return 0, err
	}
	for _, sibling := range tc.parent.kids(nsWord, "tc") {
		if sibling == tc {
			break
		}
		span, err := gridSpan(sibling)
		if err != nil {
			return 0, err
		}
		offset += span
	}
	return offset, nil
}

func cellAbove(tc *element) (*element, error) {
	tr := tc.parent
	var above *element
	for _, sibling := range tr.parent.kids(nsWord, "tr") {
		if sibling == tr {
			break
		}
		above = sibling
	}
	if above == nil {
		return nil, errValue
	}
	offset, err := gridOffset(tc)
	if err != nil {
		return nil, err
	}
	remaining, err := gridBefore(above)
	if err != nil {
		return nil, err
	}
	remaining = offset - remaining
	for _, candidate := range above.kids(nsWord, "tc") {
		if remaining < 0 {
			break
		}
		if remaining == 0 {
			return candidate, nil
		}
		span, err := gridSpan(candidate)
		if err != nil {
			return nil, err
		}
		remaining -= span
	}
	return nil, errValue
}

func cellTexts(tc *element) ([]string, error) {
	if continuesMerge(tc) {
		above, err := cellAbove(tc)
		if err != nil {
			return nil, err
		}
		return cellTexts(above)
	}
	text, err := cellText(tc)
	if err != nil {
		return nil, err
	}
	span, err := gridSpan(tc)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, max(span, 0))
	for range span {
		out = append(out, text)
	}
	return out, nil
}

func docxText(data []byte) (string, error) {
	a, err := openArchive(data)
	if err != nil {
		return "", err
	}
	root, err := mainDocument(a)
	if err != nil {
		return "", err
	}
	var body *element
	if root.space == nsWord && root.local == "document" {
		body = root.kid(nsWord, "body")
	}
	if body == nil {
		return "", readError("AttributeError")
	}
	var lines []string
	for _, p := range body.kids(nsWord, "p") {
		s, err := paragraphText(p)
		if err != nil {
			return "", err
		}
		lines = append(lines, s)
	}
	for _, tbl := range body.kids(nsWord, "tbl") {
		lines = append(lines, "")
		for _, tr := range tbl.kids(nsWord, "tr") {
			var cells []string
			for _, tc := range tr.kids(nsWord, "tc") {
				texts, err := cellTexts(tc)
				if err != nil {
					return "", err
				}
				for _, t := range texts {
					cells = append(cells, py.Strip(t))
				}
			}
			lines = append(lines, strings.Join(cells, "\t"))
		}
	}
	return py.Strip(strings.Join(lines, "\n")), nil
}
