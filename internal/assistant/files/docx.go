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
	ext := strings.TrimPrefix(py.Lower(py.PathSuffix(py.PathName(partName))), ".")
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

const maxGridSpan = 63

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
	return min(n, maxGridSpan), err
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

type rowSet struct {
	rows  []*element
	index map[*element]int
}

// rowGrid is one w:tr's layout grid: where each w:tc starts, computed once however many cells of the row below
// ask for the cell above them.
type rowGrid struct {
	set       *rowSet
	index     int
	tcs       []*element
	position  map[*element]int
	beforeErr error
	offsets   []int
	errAt     int
	regular   bool
	byOffset  map[int]int
	before    int
	spans     []int
}

func newRowGrid(set *rowSet, index int, tr *element) *rowGrid {
	g := &rowGrid{set: set, index: index, tcs: tr.kids(nsWord, "tc"), regular: true, byOffset: map[int]int{}}
	g.before, g.beforeErr = gridBefore(tr)
	g.errAt = len(g.tcs)
	g.position = make(map[*element]int, len(g.tcs))
	offset := g.before
	for j, tc := range g.tcs {
		g.position[tc] = j
		g.offsets = append(g.offsets, offset)
		if _, seen := g.byOffset[offset]; !seen {
			g.byOffset[offset] = j
		}
		span, err := gridSpan(tc)
		if err != nil {
			g.errAt = j
			break
		}
		if span < 0 {
			g.regular = false
		}
		g.spans = append(g.spans, span)
		offset += span
	}
	return g
}

func (g *rowGrid) offsetOf(tc *element) (int, error) {
	if g.beforeErr != nil {
		return 0, g.beforeErr
	}
	j := g.position[tc]
	if j > g.errAt || j >= len(g.offsets) {
		return 0, errValue
	}
	return g.offsets[j], nil
}

func (g *rowGrid) cellAt(target int) (*element, error) {
	if g.beforeErr != nil {
		return nil, g.beforeErr
	}
	if g.regular {
		if j, ok := g.byOffset[target]; ok && j <= g.errAt && j < len(g.tcs) {
			return g.tcs[j], nil
		}
		return nil, errValue
	}
	remaining := target - g.before
	for j, tc := range g.tcs {
		if remaining < 0 {
			break
		}
		if remaining == 0 {
			return tc, nil
		}
		if j >= len(g.spans) {
			return nil, errValue
		}
		remaining -= g.spans[j]
	}
	return nil, errValue
}

// docxReader reads the cells of a document's tables the way python-docx's Row.cells does: a cell spanning columns
// repeats, a vMerge continuation repeats the text of the cell above.
type docxReader struct {
	sets  map[*element]*rowSet
	grids map[*element]*rowGrid
	cells map[*element][]string
	count int
}

func (r *docxReader) grid(tr *element) *rowGrid {
	if g, ok := r.grids[tr]; ok {
		return g
	}
	set := r.sets[tr.parent]
	if set == nil {
		set = &rowSet{rows: tr.parent.kids(nsWord, "tr")}
		set.index = make(map[*element]int, len(set.rows))
		for j, row := range set.rows {
			set.index[row] = j
		}
		r.sets[tr.parent] = set
	}
	g := newRowGrid(set, set.index[tr], tr)
	r.grids[tr] = g
	return g
}

func (r *docxReader) above(tc *element) (*element, error) {
	tr := tc.parent
	g := r.grid(tr)
	if g.index == 0 {
		return nil, errValue
	}
	offset, err := g.offsetOf(tc)
	if err != nil {
		return nil, err
	}
	return r.grid(g.set.rows[g.index-1]).cellAt(offset)
}

func (r *docxReader) texts(tc *element) ([]string, error) {
	if cached, ok := r.cells[tc]; ok {
		return cached, nil
	}
	var out []string
	if continuesMerge(tc) {
		above, err := r.above(tc)
		if err != nil {
			return nil, err
		}
		out, err = r.texts(above)
		if err != nil {
			return nil, err
		}
	} else {
		text, err := cellText(tc)
		if err != nil {
			return nil, err
		}
		span, err := gridSpan(tc)
		if err != nil {
			return nil, err
		}
		out = make([]string, 0, max(span, 0))
		for range span {
			out = append(out, text)
		}
	}
	r.cells[tc] = out
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
	size := 0
	var lines []string
	add := func(line string) error {
		size += len(line) + 1
		if size > maxTextBytes {
			return errTooLarge
		}
		lines = append(lines, line)
		return nil
	}
	for _, p := range body.kids(nsWord, "p") {
		s, err := paragraphText(p)
		if err != nil {
			return "", err
		}
		if err := add(s); err != nil {
			return "", err
		}
	}
	reader := &docxReader{sets: map[*element]*rowSet{}, grids: map[*element]*rowGrid{}, cells: map[*element][]string{}}
	for _, tbl := range body.kids(nsWord, "tbl") {
		if err := add(""); err != nil {
			return "", err
		}
		for _, tr := range tbl.kids(nsWord, "tr") {
			var cells []string
			rowBytes := 0
			for _, tc := range tr.kids(nsWord, "tc") {
				if reader.count++; reader.count > tableCellCap {
					return "", errTooLarge
				}
				texts, err := reader.texts(tc)
				if err != nil {
					return "", err
				}
				for _, t := range texts {
					stripped := py.Strip(t)
					rowBytes += len(stripped) + 1
					if size+rowBytes > maxTextBytes {
						return "", errTooLarge
					}
					cells = append(cells, stripped)
				}
			}
			if err := add(strings.Join(cells, "\t")); err != nil {
				return "", err
			}
		}
	}
	return py.Strip(strings.Join(lines, "\n")), nil
}
