package media

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/py"
)

const (
	KindMovie  = "movie"
	KindTV     = "tv"
	Skipped    = "(skipped)"
	NotMovie   = "(not the movie)"
	importPref = Import + "/"
	dirMode    = 0o775
)

var Kinds = []string{KindMovie, KindTV}

type PlanError string

func (e PlanError) Error() string { return string(e) }

type Item struct {
	ID      string
	Name    string
	Torrent bool
	Root    string
	Files   []string
}

type FileView struct {
	Rel     string
	Season  string
	Episode string
	Dest    string
}

type View struct {
	Item      Item
	Kind      string
	Title     string
	Year      string
	Files     []FileView
	MovieFile string
	MovieDest string
	Error     string
}

func (v View) ID() string { return v.Item.ID }

type Op struct{ Src, Dst string }

func (t Tuning) VideosUnder(root string) []string {
	if info, err := os.Stat(root); err == nil && info.Mode().IsRegular() {
		if link, err := os.Lstat(root); err == nil && link.Mode()&fs.ModeSymlink == 0 && t.IsVideo(filepath.Base(root)) {
			return []string{root}
		}
		return []string{}
	}
	walkRoot := root
	if link, err := os.Lstat(root); err == nil && link.Mode()&fs.ModeSymlink != 0 {
		walkRoot = root + "/"
	}
	out := []string{}
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && t.IsVideo(d.Name()) {
			out = append(out, path)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func Items(ctx context.Context, qb *Qbit, root string) ([]Item, string, error) {
	found := []Item{}
	errText := ""
	torrents, err := qb.Completed(ctx)
	if err != nil {
		var qbitErr QbitError
		if !errors.As(err, &qbitErr) {
			return nil, "", err
		}
		torrents, errText = nil, err.Error()
	}
	for _, t := range torrents {
		if !t.HasTag(TagMedia) || t.HasTag(TagFiled) || t.HasTag(TagIgnored) {
			continue
		}
		found = append(found, Item{ID: t.Hash, Name: t.Name, Torrent: true, Root: t.ContentPath, Files: qb.VideosUnder(t.ContentPath)})
	}
	importDir := filepath.Join(root, Import)
	if info, err := os.Stat(importDir); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(importDir)
		for _, entry := range entries {
			entryRoot := filepath.Join(importDir, entry.Name())
			if files := qb.VideosUnder(entryRoot); len(files) > 0 {
				found = append(found, Item{ID: importPref + entry.Name(), Name: entry.Name(), Root: entryRoot, Files: files})
			}
		}
	}
	return found, errText, nil
}

func Find(ctx context.Context, qb *Qbit, root, itemID string) (Item, error) {
	found, errText, err := Items(ctx, qb, root)
	if err != nil {
		return Item{}, err
	}
	for _, it := range found {
		if it.ID == itemID {
			return it, nil
		}
	}
	if errText != "" {
		return Item{}, PlanError(errText)
	}
	return Item{}, PlanError(itemID + " is no longer waiting")
}

func (t Tuning) showYear(root, title string) string {
	tv := filepath.Join(root, TV)
	entries, err := os.ReadDir(tv)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		r := t.ParseRelease(entry.Name())
		info, err := os.Stat(filepath.Join(tv, entry.Name()))
		if err == nil && info.IsDir() && r.Year != "" && py.Casefold(r.Title) == py.Casefold(title) {
			return r.Year
		}
	}
	return ""
}

func largest(files []string) string {
	best, size := "", int64(-1)
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		if info.Size() > size {
			best, size = f, info.Size()
		}
	}
	return best
}

func relTo(item Item, path string) string {
	root, err := filepath.Abs(item.Root)
	if err == nil {
		var target string
		if target, err = filepath.Abs(path); err == nil {
			var r string
			if r, err = filepath.Rel(root, target); err == nil && r != "." && !strings.HasPrefix(r, "..") {
				return r
			}
		}
	}
	return filepath.Base(path)
}

func (t Tuning) Defaults(item Item, root string) View {
	r := t.ParseRelease(item.Name)
	view := View{Item: item, Kind: KindMovie, Title: r.Title, Year: r.Year}
	for _, f := range item.Files {
		fr := t.ParseRelease(filepath.Base(f))
		fv := FileView{Rel: relTo(item, f)}
		if fr.Episode > 0 {
			if view.Kind == KindMovie && fr.Title != "" {
				view.Title, view.Year = fr.Title, fr.Year
			}
			view.Kind = KindTV
			fv.Season, fv.Episode = strconv.Itoa(fr.Season), strconv.Itoa(fr.Episode)
		}
		view.Files = append(view.Files, fv)
	}
	if view.Kind == KindTV && view.Year == "" {
		view.Year = t.showYear(root, view.Title)
	}
	return view
}

func Plan(view *View, root string) ([]Op, error) {
	var ops []Op
	if len(view.Item.Files) == 0 {
		return nil, PlanError("no video files found")
	}
	if view.Kind == KindMovie {
		src := largest(view.Item.Files)
		dst, err := MoviePath(view.Title, view.Year, Ext(src))
		if err != nil {
			return nil, PlanError(err.Error())
		}
		view.MovieFile, view.MovieDest = relTo(view.Item, src), dst
		ops = append(ops, Op{src, dst})
	} else {
		for i := range view.Files {
			fv := &view.Files[i]
			src := view.Item.Files[i]
			if py.Strip(fv.Episode) == "" {
				fv.Dest = Skipped
				continue
			}
			season, okSeason := py.ParseInt(fv.Season)
			episode, okEpisode := py.ParseInt(fv.Episode)
			if !okSeason || !okEpisode {
				return nil, PlanError(fv.Rel + ": season and episode must be numbers")
			}
			dst, err := EpisodePath(view.Title, view.Year, int(season), int(episode), Ext(fv.Rel))
			if err != nil {
				return nil, PlanError(fv.Rel + ": " + err.Error())
			}
			fv.Dest = dst
			ops = append(ops, Op{src, dst})
		}
		if len(ops) == 0 {
			return nil, PlanError("no episodes to file")
		}
	}
	seen := map[string]bool{}
	for _, op := range ops {
		if seen[op.Dst] {
			return nil, PlanError(op.Dst + " is the destination of two files")
		}
		seen[op.Dst] = true
		if _, err := os.Lstat(filepath.Join(root, op.Dst)); err == nil {
			return nil, PlanError(op.Dst + " already exists")
		}
	}
	return ops, nil
}

func makeDirs(path string, mode os.FileMode) error {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return nil
	}
	if parent := filepath.Dir(path); parent != path {
		if _, err := os.Stat(parent); err != nil {
			if err := makeDirs(parent, 0o777); err != nil {
				return err
			}
		}
	}
	if err := os.Mkdir(path, mode); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}
	return nil
}

func (t Tuning) Apply(ctx context.Context, qb *Qbit, item Item, ops []Op, root string) error {
	for _, op := range ops {
		full := filepath.Join(root, op.Dst)
		if err := makeDirs(filepath.Dir(full), dirMode); err != nil {
			return err
		}
		if item.Torrent {
			if err := os.Link(op.Src, full); err != nil {
				return err
			}
		} else if err := os.Rename(op.Src, full); err != nil {
			return err
		}
	}
	if item.Torrent {
		return qb.AddTags(ctx, item.ID, TagFiled)
	}
	if len(t.VideosUnder(item.Root)) == 0 {
		return os.RemoveAll(item.Root)
	}
	return nil
}

func (t Tuning) Planned(view View, root string) View {
	shown := view
	shown.Files = slices.Clone(view.Files)
	if _, err := Plan(&shown, root); err != nil {
		var planErr PlanError
		if errors.As(err, &planErr) {
			shown.Error = string(planErr)
		}
	}
	return shown
}

func (t Tuning) Edited(item Item, root string, form map[string]string) View {
	view := t.Defaults(item, root)
	view.Kind = KindMovie
	if k := form["kind"]; slices.Contains(Kinds, k) {
		view.Kind = k
	}
	view.Title, view.Year = py.Strip(form["title"]), py.Strip(form["year"])
	for i := range view.Files {
		if _, ok := form[fmt.Sprintf("e%d", i)]; ok {
			view.Files[i].Season = py.Strip(form[fmt.Sprintf("s%d", i)])
			view.Files[i].Episode = py.Strip(form[fmt.Sprintf("e%d", i)])
		}
	}
	return view
}

func Ignore(ctx context.Context, qb *Qbit, item Item) error {
	if !item.Torrent {
		return PlanError("Only torrents can be ignored; delete the import entry instead.")
	}
	return qb.AddTags(ctx, item.ID, TagIgnored)
}

type SweepResult struct {
	Removed []string
	Errors  []string
}

func Sweep(ctx context.Context, qb *Qbit) (SweepResult, error) {
	out := SweepResult{Removed: []string{}, Errors: []string{}}
	torrents, err := qb.Completed(ctx)
	if err != nil {
		var qbitErr QbitError
		if !errors.As(err, &qbitErr) {
			return out, err
		}
		out.Errors = append(out.Errors, err.Error())
		return out, nil
	}
	for _, t := range torrents {
		if !t.HasTag(TagFiled) || t.State != StoppedUP {
			continue
		}
		if err := qb.Delete(ctx, t.Hash, true); err != nil {
			var qbitErr QbitError
			if !errors.As(err, &qbitErr) {
				return out, err
			}
			out.Errors = append(out.Errors, t.Name+": "+err.Error())
			continue
		}
		out.Removed = append(out.Removed, t.Name)
	}
	for _, line := range out.Errors {
		log.Warn("sweep: " + line)
	}
	return out, nil
}

func (t Tuning) wanted(name string) bool { return t.IsVideo(name) || t.IsSubtitle(name) }

func SelectFiles(ctx context.Context, qb *Qbit, torrents []Torrent) ([]string, error) {
	done := []string{}
	if torrents == nil {
		var err error
		torrents, err = qb.Torrents(ctx, "", TagMedia, "")
		if err != nil {
			var qbitErr QbitError
			if errors.As(err, &qbitErr) {
				return done, nil
			}
			return done, err
		}
	}
	for _, t := range torrents {
		if !t.HasTag(TagMedia) || t.HasTag(TagSelected) {
			continue
		}
		selected, err := selectOne(ctx, qb, t)
		if err != nil {
			var qbitErr QbitError
			if !errors.As(err, &qbitErr) {
				return done, err
			}
			log.Warn(fmt.Sprintf("select %s: %v", t.Name, err))
			continue
		}
		if selected {
			done = append(done, t.Hash)
		}
	}
	return done, nil
}

func selectOne(ctx context.Context, qb *Qbit, t Torrent) (bool, error) {
	files, err := qb.Files(ctx, t.Hash)
	if err != nil || len(files) == 0 {
		return false, err
	}
	if slices.ContainsFunc(files, func(f TorrentFile) bool { return qb.wanted(f.Name) }) {
		var keep, skip []int
		for _, f := range files {
			switch {
			case qb.wanted(f.Name) && f.Priority == PrioritySkip:
				keep = append(keep, f.Index)
			case !qb.wanted(f.Name) && f.Priority != PrioritySkip:
				skip = append(skip, f.Index)
			}
		}
		if err := qb.SetPriority(ctx, t.Hash, keep, PriorityNormal); err != nil {
			return false, err
		}
		if err := qb.SetPriority(ctx, t.Hash, skip, PrioritySkip); err != nil {
			return false, err
		}
	}
	if err := qb.AddTags(ctx, t.Hash, TagSelected); err != nil {
		return false, err
	}
	if t.State == StoppedDL || t.State == "pausedDL" {
		if err := qb.Start(ctx, t.Hash); err != nil {
			return false, err
		}
	}
	return true, nil
}
