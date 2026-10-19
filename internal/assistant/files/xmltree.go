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

	"github.com/jeiang/atrium/internal/py"
)

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
			e := &element{space: t.Name.Space, local: t.Name.Local, attrs: t.Attr, parent: cur}
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
	files map[string]*zip.File
}

func openArchive(data []byte) (*archive, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errBadZip
	}
	a := &archive{files: make(map[string]*zip.File, len(zr.File))}
	for _, f := range zr.File {
		a.files[f.Name] = f
	}
	return a, nil
}

func (a *archive) has(name string) bool { _, ok := a.files[name]; return ok }

func (a *archive) open(name string) (io.ReadCloser, error) {
	f, ok := a.files[name]
	if !ok {
		return nil, errKey
	}
	rc, err := f.Open()
	if err != nil {
		return nil, errBadZip
	}
	return rc, nil
}

func (a *archive) read(name string) ([]byte, error) {
	rc, err := a.open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
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
