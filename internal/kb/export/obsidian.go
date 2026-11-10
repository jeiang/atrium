package export

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/kb"
	"github.com/jeiang/atrium/internal/kb/render"
)

const TargetObsidian = "obsidian"

func runObsidian(ctx context.Context, d *db.DB, env config.Env, now float64) (Result, error) {
	dir, err := readSetting(ctx, d, ObsidianDirKey)
	if err != nil {
		return Result{}, err
	}
	dir = filepath.Clean(dir)
	privateTo, err := readSetting(ctx, d, ObsidianPrivateKey)
	if err != nil {
		return Result{}, err
	}
	allow, err := readSetting(ctx, d, ObsidianKindsKey)
	if err != nil {
		return Result{}, err
	}
	records, err := kb.Exportable(ctx, d.Read, kb.ExportFilter{
		Now:            now,
		Kinds:          mask(allow),
		IncludePrivate: privateTo != "" && filepath.Clean(privateTo) == dir,
	})
	if err != nil {
		return Result{}, err
	}
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return Result{}, err
	}
	base := config.From(ctx).PublicURL(env)
	titles, err := threadTitles(ctx, d.Read, records)
	if err != nil {
		return Result{}, err
	}
	entries := make([]pageEntry, 0, len(records)+1)
	for _, rec := range records {
		src := render.Source{PublicURL: base, Date: date(rec.UpdatedAt, loc)}
		if rec.SourceThreadID != nil {
			src.ThreadTitle = titles[*rec.SourceThreadID]
		}
		entries = append(entries, pageEntry{
			recordID: rec.ID,
			page:     render.RecordPage(rec.Record, src),
			path:     filepath.Join(rec.Scope, slug(rec.Title)+"-"+kb.Cite(rec.ID)+".md"),
			front:    frontMatter(rec, src),
		})
	}
	entries = append(entries, pageEntry{recordID: 0, page: render.Index(indexEntries(records, loc)), path: "index.md"})
	return syncObsidian(ctx, d, now, dir, entries)
}

func syncObsidian(ctx context.Context, d *db.DB, now float64, dir string, entries []pageEntry) (Result, error) {
	var out Result
	stored, err := loadPages(ctx, d.Read, TargetObsidian)
	if err != nil {
		return out, err
	}
	current := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		current[entry.recordID] = true
		prior, found := stored[entry.recordID]
		if found && prior.hash == entry.page.Hash && prior.remoteID == entry.path {
			out.Skipped++
			continue
		}
		if err := writeFile(dir, entry.path, []byte(entry.front+entry.page.Markdown)); err != nil {
			return out, err
		}
		if found && prior.remoteID != entry.path {
			if err := removeFile(dir, prior.remoteID); err != nil {
				return out, err
			}
		}
		if err := savePage(ctx, d.Write, TargetObsidian, entry.recordID, entry.path, entry.page.Hash, now); err != nil {
			return out, err
		}
		if found {
			out.Updated++
		} else {
			out.Created++
		}
	}
	for recordID, prior := range stored {
		if current[recordID] {
			continue
		}
		if err := removeFile(dir, prior.remoteID); err != nil {
			return out, err
		}
		if err := deletePage(ctx, d.Write, TargetObsidian, recordID); err != nil {
			return out, err
		}
		out.Removed++
	}
	return out, nil
}

func within(dir, rel string) (string, error) {
	base := filepath.Clean(dir) + string(os.PathSeparator)
	full := filepath.Clean(filepath.Join(dir, rel))
	if !strings.HasPrefix(full, base) {
		return "", fmt.Errorf("export: %s is outside %s", rel, dir)
	}
	return full, nil
}

func writeFile(dir, rel string, content []byte) error {
	full, err := within(dir, rel)
	if err != nil {
		return err
	}
	parent := filepath.Dir(full)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(parent, ".atrium-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	remove := func() { _ = os.Remove(name) }
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		remove()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		remove()
		return err
	}
	if err := tmp.Close(); err != nil {
		remove()
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		remove()
		return err
	}
	if err := os.Rename(name, full); err != nil {
		remove()
		return err
	}
	return nil
}

func removeFile(dir, rel string) error {
	full, err := within(dir, rel)
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func slug(title string) string {
	var b strings.Builder
	last := false
	for _, r := range strings.ToLower(title) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			last = false
			continue
		}
		if !last && b.Len() > 0 {
			b.WriteByte('-')
			last = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 60 {
		out = strings.TrimRight(out[:60], "-")
	}
	if out == "" {
		return "record"
	}
	return out
}

func yamlString(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}

func frontMatter(rec kb.ExportRecord, src render.Source) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("atrium_id: " + kb.Cite(rec.ID) + "\n")
	b.WriteString("title: " + yamlString(strings.Join(strings.Fields(rec.Title), " ")) + "\n")
	b.WriteString("scope: " + rec.Scope + "\n")
	b.WriteString("writer: " + rec.Writer + "\n")
	b.WriteString("confirmed: " + strconv.FormatBool(rec.Confirmed()) + "\n")
	b.WriteString("updated: " + src.Date + "\n")
	if url := render.SourceURL(rec.Record, src); url != "" {
		b.WriteString("source: " + yamlString(url) + "\n")
	} else if rec.Origin != nil {
		b.WriteString("source: " + yamlString(*rec.Origin) + "\n")
	}
	b.WriteString("---\n\n")
	return b.String()
}
