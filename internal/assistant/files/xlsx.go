package files

import (
	"encoding/xml"
	"errors"
	"io"
	"math/big"
	"path"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	nsSheet      = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
	nsOfficeRels = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

	typeSharedStrings = "application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"
	typeBook          = "application/vnd.openxmlformats-officedocument.spreadsheetml.%s.main+xml"
	typeBookMacro     = "application/vnd.ms-excel.%s.macroEnabled.main+xml"

	defaultWorkbookPart = "/xl/workbook.xml"
	stylesPart          = "xl/styles.xml"
	maxColumns          = 18278
)

var workbookTypes = []string{
	strings.Replace(typeBookMacro, "%s", "template", 1),
	strings.Replace(typeBook, "%s", "template", 1),
	strings.Replace(typeBookMacro, "%s", "sheet", 1),
	strings.Replace(typeBook, "%s", "sheet", 1),
}

type sheetRef struct {
	title string
	id    string
}

func stream(r io.Reader, want func(depth int, name xml.Name) bool, fn func(e *element) error) error {
	dec := xml.NewDecoder(r)
	depth := 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return errParse
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if want(depth, t.Name) {
				e, err := subtree(dec, t, nil)
				if err != nil {
					return err
				}
				if err := fn(e); err != nil {
					return err
				}
				continue
			}
			depth++
		case xml.EndElement:
			depth--
		}
	}
}

func subtree(dec *xml.Decoder, start xml.StartElement, parent *element) (*element, error) {
	root := &element{space: start.Name.Space, local: start.Name.Local, attrs: start.Attr, parent: parent}
	cur := root
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, errParse
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{space: t.Name.Space, local: t.Name.Local, attrs: t.Attr, parent: cur}
			cur.children = append(cur.children, e)
			cur = e
		case xml.EndElement:
			if cur == root {
				return root, nil
			}
			cur = cur.parent
		case xml.CharData:
			if len(cur.children) == 0 {
				cur.text += string(t)
			}
		}
	}
}

func richText(si *element) string {
	var b strings.Builder
	if t := lastKid(si, "t"); t != nil {
		b.WriteString(t.text)
	}
	for _, r := range si.kids(nsSheet, "r") {
		if t := lastKid(r, "t"); t != nil {
			b.WriteString(t.text)
		}
	}
	return b.String()
}

func lastKid(e *element, local string) *element {
	kids := e.kids(nsSheet, local)
	if len(kids) == 0 {
		return nil
	}
	return kids[len(kids)-1]
}

func sharedStrings(a *archive, types *element) ([]string, error) {
	var part string
	for _, o := range types.kids(nsContentTypes, "Override") {
		if kind, _ := o.attr("", "ContentType"); kind == typeSharedStrings {
			part, _ = o.attr("", "PartName")
			break
		}
	}
	if part == "" {
		return nil, nil
	}
	rc, err := a.open(strings.TrimPrefix(part, "/"))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	var out []string
	err = stream(rc, func(_ int, n xml.Name) bool { return n.Space == nsSheet && n.Local == "si" }, func(si *element) error {
		out = append(out, strings.ReplaceAll(richText(si), "x005F_", ""))
		return nil
	})
	return out, err
}

func workbookPart(types *element) (string, error) {
	for _, want := range workbookTypes {
		for _, o := range types.kids(nsContentTypes, "Override") {
			if kind, _ := o.attr("", "ContentType"); kind == want {
				name, _ := o.attr("", "PartName")
				return name, nil
			}
		}
	}
	for _, want := range workbookTypes {
		for _, d := range types.kids(nsContentTypes, "Default") {
			if kind, _ := d.attr("", "ContentType"); kind == want {
				return defaultWorkbookPart, nil
			}
		}
	}
	return "", errOS
}

func relsPath(part string) string {
	return path.Join(path.Dir(part), "_rels", path.Base(part)+".rels")
}

func targetOf(relsFile string, r relationship) string {
	if r.external {
		return r.target
	}
	if strings.HasPrefix(r.target, "/") {
		return r.target[1:]
	}
	parent := path.Dir(path.Dir(relsFile))
	if parent == "." {
		parent = ""
	}
	return path.Clean(path.Join(parent, r.target))
}

func sheetsOf(a *archive, types *element) ([]sheetRef, []string, bool, error) {
	part, err := workbookPart(types)
	if err != nil {
		return nil, nil, false, err
	}
	name := strings.TrimPrefix(part, "/")
	root, err := a.tree(name)
	if err != nil {
		return nil, nil, false, err
	}
	date1904 := false
	if props := root.kid(nsSheet, "workbookPr"); props != nil {
		v, _ := props.attr("", "date1904")
		date1904 = v == "1" || v == "true"
	}
	var refs []sheetRef
	var targets []string
	if list := root.kid(nsSheet, "sheets"); list != nil {
		var rels []relationship
		var relsFile string
		loaded := false
		for _, s := range list.kids(nsSheet, "sheet") {
			title, ok := s.attr("", "name")
			if !ok {
				return nil, nil, false, readError("TypeError")
			}
			id, _ := s.attr(nsOfficeRels, "id")
			if id == "" {
				continue
			}
			if !loaded {
				relsFile = relsPath(name)
				rt, err := a.tree(relsFile)
				if err != nil {
					return nil, nil, false, err
				}
				rels = relationships(rt)
				loaded = true
			}
			var found *relationship
			for i := range rels {
				if rels[i].id == id {
					found = &rels[i]
					break
				}
			}
			if found == nil {
				return nil, nil, false, errKey
			}
			target := targetOf(relsFile, *found)
			if !a.has(target) {
				continue
			}
			if strings.Contains(found.kind, "chartsheet") {
				if !a.has(relsPath(target)) {
					return nil, nil, false, readError("AttributeError")
				}
				continue
			}
			refs = append(refs, sheetRef{title: title, id: id})
			targets = append(targets, target)
		}
	}
	return refs, targets, date1904, nil
}

type styleFormats struct {
	date, timedelta map[int]bool
}

func readStyles(a *archive) (styleFormats, error) {
	formats := styleFormats{date: map[int]bool{}, timedelta: map[int]bool{}}
	if !a.has(stylesPart) {
		return formats, nil
	}
	root, err := a.tree(stylesPart)
	if err != nil {
		return formats, err
	}
	custom := map[int]string{}
	if list := root.kid(nsSheet, "numFmts"); list != nil {
		for _, f := range list.kids(nsSheet, "numFmt") {
			rawID, _ := f.attr("", "numFmtId")
			id, err := decimal(rawID)
			if err != nil {
				return formats, err
			}
			code, ok := f.attr("", "formatCode")
			if !ok {
				return formats, readError("TypeError")
			}
			custom[id] = code
		}
	}
	xfs := root.kid(nsSheet, "cellXfs")
	if xfs == nil {
		return formats, nil
	}
	for i, xf := range xfs.kids(nsSheet, "xf") {
		id := 0
		if raw, ok := xf.attr("", "numFmtId"); ok {
			if id, err = decimal(raw); err != nil {
				return formats, err
			}
		}
		code, ok := custom[id]
		if !ok {
			code, ok = builtinFormats[id]
		}
		if !ok {
			continue
		}
		if isDateFormat(code) {
			formats.date[i] = true
		}
		if isTimedeltaFormat(code) {
			formats.timedelta[i] = true
		}
	}
	return formats, nil
}

func xlsxText(data []byte) (string, error) {
	a, err := openArchive(data)
	if err != nil {
		return "", err
	}
	types, err := a.tree(contentTypes)
	if err != nil {
		return "", err
	}
	shared, err := sharedStrings(a, types)
	if err != nil {
		return "", err
	}
	sheets, targets, date1904, err := sheetsOf(a, types)
	if err != nil {
		return "", err
	}
	formats, err := readStyles(a)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for i, sheet := range sheets {
		out.WriteString("# Sheet: " + sheet.title + "\n")
		rc, err := a.open(targets[i])
		if err != nil {
			return "", err
		}
		sp := &sheetParser{shared: shared, formats: formats, date1904: date1904, out: &out}
		err = sp.run(rc)
		_ = rc.Close()
		if err != nil {
			return "", err
		}
		out.WriteString("\n")
	}
	return py.Strip(out.String()), nil
}

func writeCSVRow(out *strings.Builder, fields []string) {
	if len(fields) == 1 && fields[0] == "" {
		out.WriteString("\"\"\n")
		return
	}
	for i, f := range fields {
		if i > 0 {
			out.WriteByte(',')
		}
		if strings.ContainsAny(f, ",\"\r\n") {
			out.WriteByte('"')
			out.WriteString(strings.ReplaceAll(f, "\"", "\"\""))
			out.WriteByte('"')
		} else {
			out.WriteString(f)
		}
	}
	out.WriteByte('\n')
}

type cell struct {
	column int
	value  string
	set    bool
}

type sheetParser struct {
	shared   []string
	formats  styleFormats
	date1904 bool
	out      *strings.Builder

	maxCol, maxRow int
	sized          bool
	rowCounter     int
	colCounter     int

	counter int
}

func (p *sheetParser) run(r io.Reader) error {
	p.counter = 1
	inData := false
	err := stream(r, func(depth int, n xml.Name) bool {
		if n.Space != nsSheet {
			return false
		}
		return n.Local == "dimension" && depth == 1 || n.Local == "row" && depth == 2
	}, func(e *element) error {
		switch e.local {
		case "dimension":
			if p.sized || inData {
				return nil
			}
			return p.setDimension(e)
		default:
			inData = true
			return p.row(e)
		}
	})
	if errors.Is(err, errStop) {
		return nil
	}
	return err
}

func (p *sheetParser) setDimension(e *element) error {
	ref, ok := e.attr("", "ref")
	if !ok {
		return readError("TypeError")
	}
	col, row, err := dimensionMax(ref)
	if err != nil {
		return err
	}
	p.maxCol, p.maxRow, p.sized = col, row, true
	return nil
}

func (p *sheetParser) emit(cells []cell) {
	width := p.maxCol
	if !p.sized {
		width = 0
		if n := len(cells); n > 0 {
			width = cells[n-1].column
		}
	}
	if len(cells) == 0 && !p.sized {
		writeCSVRow(p.out, nil)
		return
	}
	fields := make([]string, width)
	for _, c := range cells {
		if c.column >= 1 && c.column <= width && c.set {
			fields[c.column-1] = c.value
		}
	}
	writeCSVRow(p.out, fields)
}

func (p *sheetParser) row(e *element) error {
	if raw, ok := e.attr("", "r"); ok {
		n, err := rowNumber(raw)
		if err != nil {
			return err
		}
		p.rowCounter = n
	} else {
		p.rowCounter++
	}
	p.colCounter = 0
	var cells []cell
	for _, c := range e.children {
		parsed, err := p.cell(c)
		if err != nil {
			return err
		}
		cells = append(cells, parsed)
	}
	idx := p.rowCounter
	if p.sized && idx > p.maxRow {
		for ; p.counter <= p.maxRow; p.counter++ {
			p.emit(nil)
		}
		return errStop
	}
	for ; p.counter < idx; p.counter++ {
		p.emit(nil)
	}
	if p.counter <= idx {
		p.emit(cells)
		p.counter++
	}
	return nil
}

var errStop = errors.New("stop")

func rowNumber(raw string) (int, error) {
	if n, err := strconv.Atoi(py.Strip(raw)); err == nil {
		return n, nil
	}
	f, err := strconv.ParseFloat(py.Strip(raw), 64)
	if err != nil || f != float64(int(f)) {
		return 0, errValue
	}
	return int(f), nil
}

func columnIndex(col string) (int, error) {
	if len(col) > 3 || col == "" {
		return 0, errValue
	}
	idx := 0
	for _, c := range strings.ToUpper(col) {
		if c < 'A' || c > 'Z' {
			return 0, errValue
		}
		idx = idx*26 + int(c-'A') + 1
	}
	if idx <= 0 || idx > maxColumns {
		return 0, errValue
	}
	return idx, nil
}

func coordinate(ref string) (row, column int, err error) {
	split := len(ref) - 1
	for i := range len(ref) {
		if ref[i] >= '0' && ref[i] <= '9' {
			split = i
			break
		}
	}
	if len(ref) == 0 {
		return 0, 0, errValue
	}
	column, err = columnIndex(ref[:split])
	if err != nil {
		return 0, 0, err
	}
	row, err = strconv.Atoi(ref[split:])
	if err != nil {
		return 0, 0, errValue
	}
	return row, column, nil
}

func dimensionMax(ref string) (col, row int, err error) {
	clean := strings.ReplaceAll(ref, "$", "")
	first, last, ranged := strings.Cut(clean, ":")
	if !ranged {
		last = first
	}
	for _, part := range []string{first, last} {
		letters := strings.TrimRight(part, "0123456789")
		digits := part[len(letters):]
		if letters == "" || digits == "" {
			return 0, 0, errValue
		}
	}
	row, col, err = coordinate(last)
	return col, row, err
}

func (p *sheetParser) cell(e *element) (cell, error) {
	kind := "n"
	if t, ok := e.attr("", "t"); ok {
		kind = t
	}
	var style int
	if s, ok := e.attr("", "s"); ok && s != "" {
		n, err := strconv.Atoi(py.Strip(s))
		if err != nil {
			return cell{}, errValue
		}
		style = n
	}
	value, hasValue := "", false
	if kind != "inlineStr" {
		if v := e.kid(nsSheet, "v"); v != nil && v.text != "" {
			value, hasValue = v.text, true
		}
	}
	var column int
	if ref, ok := e.attr("", "r"); ok && ref != "" {
		var err error
		_, column, err = coordinate(ref)
		if err != nil {
			return cell{}, err
		}
		p.colCounter = column
	} else {
		p.colCounter++
		column = p.colCounter
	}
	out := cell{column: column}
	if !hasValue {
		if kind == "inlineStr" {
			if is := e.kid(nsSheet, "is"); is != nil {
				out.value, out.set = richText(is), true
			}
		}
		return out, nil
	}
	text, err := p.convert(kind, value, style)
	if err != nil {
		return cell{}, err
	}
	out.value, out.set = text, true
	return out, nil
}

func (p *sheetParser) convert(kind, value string, style int) (string, error) {
	switch kind {
	case "n":
		number, err := castNumber(value)
		if err != nil {
			return "", err
		}
		if p.formats.date[style] {
			if text, ok := fromExcel(number.float(), p.date1904, p.formats.timedelta[style]); ok {
				return text, nil
			}
			return "#VALUE!", nil
		}
		return number.String(), nil
	case "s":
		n, err := strconv.Atoi(py.Strip(value))
		if err != nil {
			return "", errValue
		}
		if n < 0 {
			n += len(p.shared)
		}
		if n < 0 || n >= len(p.shared) {
			return "", errIndex
		}
		return p.shared[n], nil
	case "b":
		n, err := strconv.Atoi(py.Strip(value))
		if err != nil {
			return "", errValue
		}
		if n != 0 {
			return "True", nil
		}
		return "False", nil
	case "d":
		return fromISO8601(value)
	}
	return value, nil
}

type number struct {
	isInt bool
	i     *big.Int
	f     float64
}

func (n number) String() string {
	if n.isInt {
		return n.i.String()
	}
	return py.FloatRepr(n.f)
}

func (n number) float() float64 {
	if n.isInt {
		f, _ := new(big.Float).SetInt(n.i).Float64()
		return f
	}
	return n.f
}

func castNumber(value string) (number, error) {
	s := py.Strip(value)
	if strings.ContainsAny(value, ".Ee") {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil && !errors.Is(err, strconv.ErrRange) {
			return number{}, errValue
		}
		return number{f: f}, nil
	}
	i, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return number{}, errValue
	}
	return number{isInt: true, i: i}, nil
}
