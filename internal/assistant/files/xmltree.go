package files

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strconv"
	"strings"

	actx "github.com/jeiang/atrium/internal/assistant/context"
	"github.com/jeiang/atrium/internal/py"
)

// maxTextBytes is the most text any attachment can carry into a request: the compaction budget of the largest
// context at four bytes a character.
var maxTextBytes = int(4 * actx.CharsPerToken * actx.LimitsFor(actx.ModelContext).Compact)

// readError carries the name of the Python exception the same failure raises in the frozen app; the operator sees
// it after "could not read the Word file: ".
type readError string

func (e readError) Error() string { return string(e) }

func errorName(err error) string {
	var re readError
	if errors.As(err, &re) {
		return string(re)
	}
	return "Exception"
}

// limitError is a document that goes past a Phase A resource cap (decompressed part size, Excel's grid, extracted
// text); the frozen Python extractor has no such caps.
type limitError string

func (e limitError) Error() string { return string(e) }

const (
	treePartCap   = 16 << 20
	streamPartCap = FileCapBytes
	archiveCap    = 128 << 20
	tableCellCap  = 1 << 20
	errTooLarge   = limitError("the document is too large to read")
)

const (
	errBadZip = readError("BadZipFile")
	errKey    = readError("KeyError")
	errValue  = readError("ValueError")
	errOS     = readError("OSError")
	errSyntax = readError("XMLSyntaxError")
	errParse  = readError("ParseError")
	errIndex  = readError("IndexError")
)

type element struct {
	space, local string
	attrs        []xml.Attr
	children     []*element
	parent       *element
	text         string
}

func parseXML(data []byte) (*element, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var root, cur *element
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errSyntax
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &element{space: t.Name.Space, local: t.Name.Local, attrs: normalizeAttrs(t.Attr), parent: cur}
			if cur != nil {
				cur.children = append(cur.children, e)
			} else if root == nil {
				root = e
			}
			cur = e
		case xml.EndElement:
			if cur != nil {
				cur = cur.parent
			}
		case xml.CharData:
			if cur != nil && len(cur.children) == 0 {
				cur.text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errSyntax
	}
	return root, nil
}

var attrSpace = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ")

// normalizeAttrs applies XML's attribute-value normalization, which encoding/xml leaves out.
func normalizeAttrs(attrs []xml.Attr) []xml.Attr {
	for i := range attrs {
		attrs[i].Value = attrSpace.Replace(attrs[i].Value)
	}
	return attrs
}

func (e *element) attr(space, local string) (string, bool) {
	for _, a := range e.attrs {
		if a.Name.Local == local && a.Name.Space == space {
			return a.Value, true
		}
	}
	return "", false
}

func (e *element) kids(space, local string) []*element {
	var out []*element
	for _, c := range e.children {
		if c.space == space && c.local == local {
			out = append(out, c)
		}
	}
	return out
}

func (e *element) kid(space, local string) *element {
	for _, c := range e.children {
		if c.space == space && c.local == local {
			return c
		}
	}
	return nil
}

func decimal(s string) (int, error) {
	n, err := strconv.Atoi(py.Strip(s))
	if err != nil {
		return 0, errValue
	}
	return n, nil
}

type archive struct {
	files     map[string]*zip.File
	remaining int64
}

func openArchive(data []byte) (*archive, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errBadZip
	}
	a := &archive{files: make(map[string]*zip.File, len(zr.File)), remaining: archiveCap}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}
	return a, nil
}

func (a *archive) has(name string) bool { _, ok := a.files[name]; return ok }

type cappedReader struct {
	rc   io.ReadCloser
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if c.left <= 0 {
		n, err := c.rc.Read(p[:1])
		if n > 0 {
			return 0, errTooLarge
		}
		return 0, err
	}
	n, err := c.rc.Read(p[:min(int64(len(p)), c.left)])
	c.left -= int64(n)
	return n, err
}

func (c *cappedReader) Close() error { return c.rc.Close() }

func (a *archive) open(name string, limit int64) (io.ReadCloser, error) {
	f, ok := a.files[name]
	if !ok {
		return nil, errKey
	}
	if f.UncompressedSize64 > uint64(limit) || int64(f.UncompressedSize64) > a.remaining {
		return nil, errTooLarge
	}
	a.remaining -= int64(f.UncompressedSize64)
	rc, err := f.Open()
	if err != nil {
		return nil, errBadZip
	}
	return &cappedReader{rc: rc, left: limit}, nil
}

func (a *archive) read(name string) ([]byte, error) {
	rc, err := a.open(name, treePartCap)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if errors.Is(err, errTooLarge) {
		return nil, errTooLarge
	}
	if err != nil {
		return nil, errBadZip
	}
	return data, nil
}

func (a *archive) tree(name string) (*element, error) {
	data, err := a.read(name)
	if err != nil {
		return nil, err
	}
	return parseXML(data)
}

const (
	nsContentTypes = "http://schemas.openxmlformats.org/package/2006/content-types"
	nsRelations    = "http://schemas.openxmlformats.org/package/2006/relationships"
	contentTypes   = "[Content_Types].xml"
)

type relationship struct {
	id, kind, target string
	external         bool
}

func relationships(root *element) []relationship {
	var out []relationship
	for _, r := range root.kids(nsRelations, "Relationship") {
		var rel relationship
		rel.id, _ = r.attr("", "Id")
		rel.kind, _ = r.attr("", "Type")
		rel.target, _ = r.attr("", "Target")
		mode, _ := r.attr("", "TargetMode")
		rel.external = mode == "External"
		out = append(out, rel)
	}
	return out
}

func resolvePart(base, target string) string {
	if strings.HasPrefix(target, "/") {
		return path.Clean(target)
	}
	return path.Clean(path.Join(base, target))
}
