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
	"syscall"

	"github.com/jeiang/atrium/internal/ledger"
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
		if !IsQbit(err) {
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

func (t Tuning) Apply(ctx context.Context, store *ledger.Store, set Settings, item Item, ops []Op, root string) error {
	if len(ops) == 0 {
		return PlanError("no files to place")
	}
	plan := filePlan{Item: item.ID, Torrent: item.Torrent, Root: root}
	for _, op := range ops {
		info, err := os.Stat(op.Src)
		if err != nil {
			return err
		}
		full := filepath.Join(root, op.Dst)
		if _, err := os.Lstat(full); err == nil {
			return PlanError(op.Dst + " already exists")
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return PlanError("cannot identify " + op.Src)
		}
		plan.Steps = append(plan.Steps, fileStep{
			Src: op.Src, Dst: full, Rel: op.Dst, Link: item.Torrent,
			Dev: uint64(st.Dev), Ino: st.Ino, Size: info.Size(),
		})
	}
	confirmed := []string{}
	for _, step := range plan.Steps {
		ticket, err := store.Begin(ctx, ledger.New{
			Origin: "media", Operation: OpFile,
			OperationKey: "media:file:" + item.ID + ":" + step.Rel + ":" + ledger.NewKey(),
			Target:       item.ID, Action: jsonOf(plan),
			Before:    jsonOf(map[string]any{"confirmed": confirmed, "dst": step.Rel}),
			Undo:      jsonOf(fileUndo{Op: UndoFilePlace, Step: step}),
			Resources: []ledger.Resource{{Key: "file:" + step.Dst}},
		}, nil)
		if err != nil {
			return err
		}
		if err := place(step); err != nil {
			_ = store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{State: ledger.Failed, Target: item.ID, Error: err.Error()}, nil)
			return err
		}
		if err := store.Settle(context.WithoutCancel(ctx), ticket, ledger.Result{
			State: ledger.Done, Target: item.ID, After: jsonOf(map[string]any{"placed": step.Rel}),
		}, nil); err != nil {
			return err
		}
		confirmed = append(confirmed, step.Rel)
	}
	if item.Torrent {
		if err := set.Tag(ctx, store, set, Scope{}, item.ID, TagIgnored, false); err != nil {
			return err
		}
		return set.Tag(ctx, store, set, Scope{}, item.ID, TagFiled, true)
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

func Ignore(ctx context.Context, store *ledger.Store, set Settings, item Item) error {
	if !item.Torrent {
		return PlanError("Only torrents can be ignored; delete the import entry instead.")
	}
	if err := set.Tag(ctx, store, set, Scope{}, item.ID, TagFiled, false); err != nil {
		return err
	}
	return set.Tag(ctx, store, set, Scope{}, item.ID, TagIgnored, true)
}

type SweepResult struct {
	Removed []string
	Errors  []string
}

func Sweep(ctx context.Context, store *ledger.Store, set Settings) (SweepResult, error) {
	out := SweepResult{Removed: []string{}, Errors: []string{}}
	qb, err := set.Qbit()
	if err != nil {
		return out, err
	}
	torrents, err := qb.Completed(ctx)
	if err != nil {
		if !IsQbit(err) {
			return out, err
		}
		out.Errors = append(out.Errors, err.Error())
		return out, nil
	}
	for _, t := range torrents {
		if !t.HasTag(TagMedia) || !t.HasTag(TagFiled) || t.State != StoppedUP {
			continue
		}
		if err := set.Delete(ctx, store, set, Scope{}, t.Hash, true); err != nil {
			var conflict *ledger.Conflict
			if !IsQbit(err) && !errors.As(err, &conflict) {
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

func (t Tuning) SelectFiles(ctx context.Context, store *ledger.Store, set Settings, scope Scope, torrents []Torrent) ([]string, error) {
	done := []string{}
	qb, err := set.Qbit()
	if err != nil {
		return done, err
	}
	if torrents == nil {
		torrents, err = qb.Torrents(ctx, "", TagMedia, "")
		if err != nil {
			if IsQbit(err) {
				return done, nil
			}
			return done, err
		}
	}
	for _, torrent := range torrents {
		if !torrent.HasTag(TagMedia) || torrent.HasTag(TagSelected) {
			continue
		}
		selected, err := t.selectOne(ctx, store, set, scope, qb, torrent)
		if err != nil {
			var conflict *ledger.Conflict
			if !IsQbit(err) && !errors.As(err, &conflict) {
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

func (t Tuning) selectOne(ctx context.Context, store *ledger.Store, set Settings, scope Scope, qb *Qbit, torrent Torrent) (bool, error) {
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
		if len(keep) > 0 {
			if err := t.SetPriority(ctx, store, set, scope, torrent.Hash, keep, PriorityNormal); err != nil {
				return false, err
			}
		}
		if len(skip) > 0 {
			if err := t.SetPriority(ctx, store, set, scope, torrent.Hash, skip, PrioritySkip); err != nil {
				return false, err
			}
		}
	}
	if err := t.Tag(ctx, store, set, scope, torrent.Hash, TagSelected, true); err != nil {
		return false, err
	}
	if torrent.State == StoppedDL || torrent.State == "pausedDL" {
		if err := t.Start(ctx, store, set, scope, torrent.Hash); err != nil {
			return false, err
		}
	}
	return true, nil
}
