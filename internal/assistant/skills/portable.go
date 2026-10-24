package skills

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/py"
)

const (
	FileName        = "SKILL.md"
	maxImportSkills = 1000
)

type Item struct {
	Source      string
	Name        string
	Description string
	Body        string
}

type Imported struct {
	Created   []string
	Updated   []string
	Unchanged []string
}

func Render(s Skill) string {
	var description bytes.Buffer
	enc := json.NewEncoder(&description)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s.Description)
	return "---\nname: " + s.Name + "\ndescription: " + strings.TrimRight(description.String(), "\n") + "\n---\n\n" + s.Body + "\n"
}

func Parse(text string) (name, description, body string, err error) {
	text = strings.ReplaceAll(strings.TrimPrefix(text, "\uFEFF"), "\r\n", "\n")
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		return "", "", "", Error("missing the --- frontmatter")
	}
	var front []string
	closed := false
	for {
		line, tail, more := strings.Cut(rest, "\n")
		if strings.TrimRight(line, " \t") == "---" {
			rest, closed = tail, true
			break
		}
		front = append(front, line)
		if !more {
			break
		}
		rest = tail
	}
	if !closed {
		return "", "", "", Error("the frontmatter is not closed with ---")
	}
	fields := frontmatter(front)
	return fields["name"], fields["description"], rest, nil
}

func frontmatter(lines []string) map[string]string {
	type field struct {
		key, value string
		more       []string
	}
	var fields []*field
	for _, line := range lines {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			if n := len(fields); n > 0 {
				fields[n-1].more = append(fields[n-1].more, strings.TrimSpace(line))
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		fields = append(fields, &field{key: strings.TrimSpace(key), value: strings.TrimSpace(value)})
	}
	out := map[string]string{}
	for _, f := range fields {
		out[f.key] = scalar(f.value, f.more)
	}
	return out
}

func scalar(value string, more []string) string {
	switch {
	case value == "":
		return strings.Join(nonEmpty(more), " ")
	case value[0] == '|' || value[0] == '>':
		sep := " "
		if value[0] == '|' {
			sep = "\n"
		}
		return strings.Join(more, sep)
	case value[0] == '"' && len(more) == 0:
		var decoded string
		if json.Unmarshal([]byte(value), &decoded) == nil {
			return decoded
		}
	case value[0] == '\'' && len(more) == 0 && len(value) >= 2 && value[len(value)-1] == '\'':
		return strings.ReplaceAll(value[1:len(value)-1], "''", "'")
	}
	return strings.Join(append([]string{value}, nonEmpty(more)...), " ")
}

func nonEmpty(parts []string) []string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type Limits struct {
	Entry int64
	Total int64
}

const (
	MaxImportBytes   = 16 << 20
	frontmatterBytes = 4096
	maxZipEntries    = 10 * maxImportSkills
)

func ReadLimits(ctx context.Context, q db.Querier) (Limits, error) {
	r := assistant.Read(ctx, q)
	body := r.Int(assistant.KeySkillBodyChars)
	if err := r.Err(); err != nil {
		return Limits{}, err
	}
	return Limits{Entry: int64(body)*utf8.UTFMax + frontmatterBytes, Total: MaxImportBytes}, nil
}

func tooLarge(source string, l Limits) Error {
	return Error(fmt.Sprintf("%s: larger than %d bytes", source, l.Entry))
}

func Collect(fsys fs.FS, l Limits) ([]Item, error) {
	var items []Item
	var total int64
	err := fs.WalkDir(fsys, ".", func(p string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Base(p) != FileName {
			return nil
		}
		if len(items) >= maxImportSkills {
			return Error(fmt.Sprintf("more than %d skills", maxImportSkills))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > l.Entry {
			return tooLarge(p, l)
		}
		file, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer func() { _ = file.Close() }()
		raw, err := io.ReadAll(io.LimitReader(file, l.Entry+1))
		if err != nil {
			return err
		}
		if int64(len(raw)) > l.Entry {
			return tooLarge(p, l)
		}
		if total += int64(len(raw)); total > l.Total {
			return Error(fmt.Sprintf("more than %d bytes of skills", l.Total))
		}
		name, description, body, err := Parse(string(raw))
		if err != nil {
			return Error(p + ": " + err.Error())
		}
		if name == "" {
			if dir := path.Dir(p); dir != "." {
				name = path.Base(dir)
			}
		}
		items = append(items, Item{Source: p, Name: name, Description: description, Body: body})
		return nil
	})
	return items, err
}

func CollectZip(data []byte, l Limits) ([]Item, error) {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, Error("not a zip archive")
	}
	if len(archive.File) > maxZipEntries {
		return nil, Error(fmt.Sprintf("more than %d files in the archive", maxZipEntries))
	}
	return Collect(archive, l)
}

func CollectFile(name string, data []byte, l Limits) ([]Item, error) {
	if int64(len(data)) > l.Entry {
		return nil, tooLarge(name, l)
	}
	skill, description, body, err := Parse(string(data))
	if err != nil {
		return nil, Error(name + ": " + err.Error())
	}
	return []Item{{Source: name, Name: skill, Description: description, Body: body}}, nil
}

func Import(ctx context.Context, d *db.DB, items []Item, now float64) (Imported, error) {
	var out Imported
	if len(items) == 0 {
		return out, Error("no " + FileName + " found")
	}
	seen := map[string]string{}
	for _, item := range items {
		name := py.Strip(item.Name)
		if first, dup := seen[name]; dup {
			return out, Error(fmt.Sprintf("%s: %q is also defined in %s", item.Source, name, first))
		}
		seen[name] = item.Source
	}
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		out = Imported{}
		for _, item := range items {
			name := py.Strip(item.Name)
			existing, err := Get(ctx, tx, name)
			if err != nil {
				return err
			}
			if existing != nil && existing.Description == strings.Join(py.Split(item.Description), " ") && existing.Body == py.Strip(item.Body) {
				out.Unchanged = append(out.Unchanged, name)
				continue
			}
			if err := Save(ctx, tx, name, item.Description, item.Body, now); err != nil {
				var refused Error
				if errors.As(err, &refused) {
					return Error(item.Source + ": " + string(refused))
				}
				return err
			}
			if existing == nil {
				out.Created = append(out.Created, name)
			} else {
				out.Updated = append(out.Updated, name)
			}
		}
		return nil
	})
	if err != nil {
		return Imported{}, err
	}
	sort.Strings(out.Created)
	sort.Strings(out.Updated)
	sort.Strings(out.Unchanged)
	return out, nil
}

func Export(ctx context.Context, q db.Querier, names []string) ([]Skill, error) {
	if len(names) == 0 {
		return List(ctx, q, "")
	}
	out := make([]Skill, 0, len(names))
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		found, err := Get(ctx, q, name)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return nil, Error("no skill named " + name)
		}
		out = append(out, *found)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func WriteZip(w io.Writer, list []Skill) error {
	archive := zip.NewWriter(w)
	for _, s := range list {
		file, err := archive.Create(s.Name + "/" + FileName)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(file, Render(s)); err != nil {
			return err
		}
	}
	return archive.Close()
}
