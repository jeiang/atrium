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

	"github.com/jeiang/atrium/internal/textutil"
)

const (
	Skipped    = "(skipped)"
	importPref = Import + "/"
	dirMode    = 0o775
)

type PlanError string

func (e PlanError) Error() string { return string(e) }

type Item struct {
	ID      string
	Name    string
	Torrent bool
	Root    string
	Files   []string
	Subs    []string
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

func (t Tuning) SubtitlesUnder(root string) []string {
	out := []string{}
	walkRoot := root
	if link, err := os.Lstat(root); err == nil && link.Mode()&fs.ModeSymlink != 0 {
		walkRoot = root + "/"
	}
	_ = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() && t.IsSubtitle(d.Name()) {
			out = append(out, path)
		}
		return nil
	})
	slices.Sort(out)
	return out
}

func (t Tuning) Items(ctx context.Context, qb *Qbit, root string) ([]Item, string, error) {
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
	for _, torrent := range torrents {
		if !torrent.HasTag(TagMedia) || torrent.HasTag(TagFiled) || torrent.HasTag(TagIgnored) {
			continue
		}
		found = append(found, Item{ID: torrent.Hash, Name: torrent.Name, Torrent: true, Root: torrent.ContentPath, Files: t.VideosUnder(torrent.ContentPath), Subs: t.SubtitlesUnder(torrent.ContentPath)})
	}
	importDir := filepath.Join(root, Import)
	if info, err := os.Stat(importDir); err == nil && info.IsDir() {
		entries, err := os.ReadDir(importDir)
		if err != nil {
			return nil, "", err
		}
		for _, entry := range entries {
			entryRoot := filepath.Join(importDir, entry.Name())
			if files := t.VideosUnder(entryRoot); len(files) > 0 {
				found = append(found, Item{ID: importPref + entry.Name(), Name: entry.Name(), Root: entryRoot, Files: files, Subs: t.SubtitlesUnder(entryRoot)})
			}
		}
	}
	return found, errText, nil
}

func (t Tuning) Find(ctx context.Context, qb *Qbit, root, itemID string) (Item, error) {
	found, errText, err := t.Items(ctx, qb, root)
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
		if err == nil && info.IsDir() && r.Year != "" && textutil.Casefold(r.Title) == textutil.Casefold(title) {
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

type PlanResult struct {
	View  View
	Ops   []Op
	Error string
}

func subtitleOps(item Item, videoSrc, videoDst string) []Op {
	stem := strings.TrimSuffix(filepath.Base(videoSrc), filepath.Ext(videoSrc))
	var out []Op
	for _, sub := range item.Subs {
		base := filepath.Base(sub)
		if !strings.HasPrefix(base, stem) {
			continue
		}
		suffix := strings.TrimPrefix(strings.TrimSuffix(base, filepath.Ext(base)), stem)
		dst := strings.TrimSuffix(videoDst, filepath.Ext(videoDst)) + suffix + filepath.Ext(base)
		out = append(out, Op{sub, dst})
	}
	return out
}

func (t Tuning) Plan(view View, root string) PlanResult {
	out := PlanResult{View: view}
	fail := func(text string) PlanResult {
		out.Error = text
		return out
	}
	if len(view.Item.Files) == 0 {
		return fail("no video files found")
	}
	if view.Kind != KindMovie && view.Kind != KindTV {
		return fail("unknown kind " + view.Kind)
	}
	if view.Kind == KindMovie {
		src := largest(view.Item.Files)
		dst, err := MoviePath(view.Title, view.Year, Ext(src))
		if err != nil {
			return fail(err.Error())
		}
		out.View.MovieFile, out.View.MovieDest = relTo(view.Item, src), dst
		out.Ops = append(out.Ops, Op{src, dst})
		out.Ops = append(out.Ops, subtitleOps(view.Item, src, dst)...)
	} else {
		for i := range out.View.Files {
			fv := &out.View.Files[i]
			src := view.Item.Files[i]
			if strings.TrimSpace(fv.Episode) == "" {
				fv.Dest = Skipped
				continue
			}
			season, okSeason := parseInt(fv.Season)
			episode, okEpisode := parseInt(fv.Episode)
			if !okSeason || !okEpisode {
				return fail(fv.Rel + ": season and episode must be numbers")
			}
			dst, err := EpisodePath(view.Title, view.Year, int(season), int(episode), Ext(fv.Rel))
			if err != nil {
				return fail(fv.Rel + ": " + err.Error())
			}
			fv.Dest = dst
			out.Ops = append(out.Ops, Op{src, dst})
			out.Ops = append(out.Ops, subtitleOps(view.Item, src, dst)...)
		}
		if len(out.Ops) == 0 {
			return fail("no episodes to file")
		}
	}
	seen := map[string]bool{}
	for _, op := range out.Ops {
		if seen[op.Dst] {
			return fail(op.Dst + " is the destination of two files")
		}
		seen[op.Dst] = true
		if _, err := os.Lstat(filepath.Join(root, op.Dst)); err == nil {
			return fail(op.Dst + " already exists")
		}
	}
	return out
}

func (t Tuning) Planned(view View, root string) View { return t.Plan(view, root).View }

func (t Tuning) Apply(ctx context.Context, qb *Qbit, item Item, ops []Op, root string) (err error) {
	var undo []func()
	defer func() {
		if err == nil {
			return
		}
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}()
	for _, op := range ops {
		full := filepath.Join(root, op.Dst)
		if err := os.MkdirAll(filepath.Dir(full), dirMode); err != nil {
			return err
		}
		if item.Torrent {
			if err := os.Link(op.Src, full); err != nil {
				return err
			}
			placed := full
			undo = append(undo, func() { _ = os.Remove(placed) })
		} else {
			if err := os.Rename(op.Src, full); err != nil {
				return err
			}
			src, dst := op.Src, full
			undo = append(undo, func() { _ = os.Rename(dst, src) })
		}
	}
	if item.Torrent {
		if err := qb.RemoveTags(ctx, item.ID, TagIgnored); err != nil {
			return err
		}
		return qb.AddTags(ctx, item.ID, TagFiled)
	}
	if len(t.VideosUnder(item.Root)) == 0 {
		return os.RemoveAll(item.Root)
	}
	return nil
}

func (t Tuning) Edited(item Item, root string, form map[string]string) View {
	view := t.Defaults(item, root)
	view.Kind = KindMovie
	if k := form["kind"]; slices.Contains(Kinds, k) {
		view.Kind = k
	}
	view.Title, view.Year = strings.TrimSpace(form["title"]), strings.TrimSpace(form["year"])
	for i := range view.Files {
		if _, ok := form[fmt.Sprintf("e%d", i)]; ok {
			view.Files[i].Season = strings.TrimSpace(form[fmt.Sprintf("s%d", i)])
			view.Files[i].Episode = strings.TrimSpace(form[fmt.Sprintf("e%d", i)])
		}
	}
	return view
}

func Ignore(ctx context.Context, qb *Qbit, item Item) error {
	if !item.Torrent {
		return PlanError("Only torrents can be ignored; delete the import entry instead.")
	}
	if err := qb.RemoveTags(ctx, item.ID, TagFiled); err != nil {
		return err
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
		if !t.HasTag(TagMedia) || !t.HasTag(TagFiled) || t.State != StoppedUP {
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

func (t Tuning) SelectFiles(ctx context.Context, qb *Qbit, torrents []Torrent) ([]string, error) {
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
	for _, torrent := range torrents {
		if !torrent.HasTag(TagMedia) || torrent.HasTag(TagSelected) {
			continue
		}
		selected, err := t.selectOne(ctx, qb, torrent)
		if err != nil {
			var qbitErr QbitError
			if !errors.As(err, &qbitErr) {
				return done, err
			}
			log.Warn(fmt.Sprintf("select %s: %v", torrent.Name, err))
			continue
		}
		if selected {
			done = append(done, torrent.Hash)
		}
	}
	return done, nil
}

func (t Tuning) selectOne(ctx context.Context, qb *Qbit, torrent Torrent) (bool, error) {
	files, err := qb.Files(ctx, torrent.Hash)
	if err != nil || len(files) == 0 {
		return false, err
	}
	if slices.ContainsFunc(files, func(f TorrentFile) bool { return t.wanted(f.Name) }) {
		var keep, skip []int
		for _, f := range files {
			switch {
			case t.wanted(f.Name) && f.Priority == PrioritySkip:
				keep = append(keep, f.Index)
			case !t.wanted(f.Name) && f.Priority != PrioritySkip:
				skip = append(skip, f.Index)
			}
		}
		if err := qb.SetPriority(ctx, torrent.Hash, keep, PriorityNormal); err != nil {
			return false, err
		}
		if err := qb.SetPriority(ctx, torrent.Hash, skip, PrioritySkip); err != nil {
			return false, err
		}
	}
	if err := qb.AddTags(ctx, torrent.Hash, TagSelected); err != nil {
		return false, err
	}
	if torrent.State == StoppedDL || torrent.State == "pausedDL" {
		if err := qb.Start(ctx, torrent.Hash); err != nil {
			return false, err
		}
	}
	return true, nil
}
