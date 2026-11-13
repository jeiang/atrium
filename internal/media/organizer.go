package media

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/media/agentclient"
	"github.com/jeiang/atrium/internal/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/textutil"
)

const (
	Skipped    = "(skipped)"
	importPref = Import + "/"
)

type PlanError string

func (e PlanError) Error() string { return string(e) }

type Item struct {
	ID      string
	Name    string
	Torrent bool
	Source  protocol.Path
	Files   []string
	Subs    []string
	Sizes   map[string]int64
	Error   string
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

func DownloadRel(prefix, content string) (string, bool) {
	prefix = strings.TrimRight(prefix, "/")
	if !strings.HasPrefix(prefix, "/") {
		return "", false
	}
	rel, ok := strings.CutPrefix(content, prefix+"/")
	rel = strings.TrimRight(rel, "/")
	if !ok || rel == "" {
		return "", false
	}
	for part := range strings.SplitSeq(rel, "/") {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00") {
			return "", false
		}
	}
	return rel, true
}

func (t Tuning) fill(ctx context.Context, agent *agentclient.Client, item *Item) error {
	src, err := agent.Source(ctx, item.Source)
	if err != nil {
		return err
	}
	item.Files, item.Subs, item.Sizes = []string{}, []string{}, map[string]int64{}
	for _, f := range src.Files {
		switch {
		case t.IsVideo(f.Path.Rel, f.Identity.Size):
			item.Files = append(item.Files, f.Path.Rel)
		case t.IsSubtitle(path.Base(f.Path.Rel)):
			item.Subs = append(item.Subs, f.Path.Rel)
		}
		item.Sizes[f.Path.Rel] = f.Identity.Size
	}
	slices.Sort(item.Files)
	slices.Sort(item.Subs)
	return nil
}

func agentText(err error) string {
	var e *agentclient.Error
	if errors.As(err, &e) {
		return "the media agent refused the source (" + e.Code + ")"
	}
	return "the media agent is unavailable"
}

func (t Tuning) Items(ctx context.Context, qb *Qbit, agent *agentclient.Client, prefix string) ([]Item, string, error) {
	found := []Item{}
	var problems []string
	torrents, err := qb.Completed(ctx)
	if err != nil {
		if !IsQbit(err) {
			return nil, "", err
		}
		torrents, problems = nil, append(problems, err.Error())
	}
	for _, torrent := range torrents {
		if !torrent.HasTag(TagMedia) || torrent.HasTag(TagFiled) || torrent.HasTag(TagIgnored) {
			continue
		}
		item := Item{ID: torrent.Hash, Name: torrent.Name, Torrent: true}
		rel, ok := DownloadRel(prefix, torrent.ContentPath)
		if !ok {
			item.Error = "the download is outside " + KeyDownloadPrefix
		} else {
			item.Source = protocol.Path{Root: protocol.RootDownloads, Rel: rel}
			if err := t.fill(ctx, agent, &item); err != nil {
				item.Error = agentText(err)
			}
		}
		found = append(found, item)
	}
	imports, err := agent.Imports(ctx)
	if err != nil {
		return found, strings.Join(append(problems, agentText(err)), "; "), nil
	}
	for _, entry := range imports {
		item := Item{ID: importPref + path.Base(entry.Rel), Name: path.Base(entry.Rel), Source: entry}
		if err := t.fill(ctx, agent, &item); err != nil {
			item.Error = agentText(err)
		}
		if item.Error != "" || len(item.Files) > 0 {
			found = append(found, item)
		}
	}
	return found, strings.Join(problems, "; "), nil
}

func (t Tuning) Find(ctx context.Context, qb *Qbit, agent *agentclient.Client, prefix, itemID string) (Item, error) {
	found, errText, err := t.Items(ctx, qb, agent, prefix)
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

func Shows(ctx context.Context, agent *agentclient.Client) []string {
	listing, err := agent.Entries(ctx, protocol.Path{Root: protocol.RootTV})
	if err != nil {
		return nil
	}
	out := []string{}
	for _, e := range listing.Entries {
		if e.Dir {
			out = append(out, e.Name)
		}
	}
	return out
}

func (t Tuning) showYear(shows []string, title string) string {
	for _, name := range shows {
		r := t.ParseRelease(name)
		if r.Year != "" && textutil.Casefold(r.Title) == textutil.Casefold(title) {
			return r.Year
		}
	}
	return ""
}

func largest(item Item, files []string) string {
	best, size := "", int64(-1)
	for _, f := range files {
		if s, ok := item.Sizes[f]; ok && s > size {
			best, size = f, s
		}
	}
	return best
}

func relTo(item Item, p string) string {
	if r, ok := strings.CutPrefix(p, item.Source.Rel+"/"); ok {
		return r
	}
	return path.Base(p)
}

func (t Tuning) Defaults(item Item, shows []string) View {
	r := t.ParseRelease(item.Name)
	view := View{Item: item, Kind: KindMovie, Title: r.Title, Year: r.Year}
	for _, f := range item.Files {
		fr := t.ParseRelease(path.Base(f))
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
		view.Year = t.showYear(shows, view.Title)
	}
	return view
}

type PlanResult struct {
	View  View
	Ops   []Op
	Error string
}

func subtitleOps(item Item, videoSrc, videoDst string) []Op {
	stem := strings.TrimSuffix(path.Base(videoSrc), path.Ext(videoSrc))
	var out []Op
	for _, sub := range item.Subs {
		base := path.Base(sub)
		if !strings.HasPrefix(base, stem) {
			continue
		}
		suffix := strings.TrimPrefix(strings.TrimSuffix(base, path.Ext(base)), stem)
		dst := strings.TrimSuffix(videoDst, path.Ext(videoDst)) + suffix + path.Ext(base)
		out = append(out, Op{sub, dst})
	}
	return out
}

func (t Tuning) Plan(view View) PlanResult {
	out := PlanResult{View: view}
	fail := func(text string) PlanResult {
		out.Error = text
		return out
	}
	if view.Item.Error != "" {
		return fail(view.Item.Error)
	}
	if len(view.Item.Files) == 0 {
		return fail("no video files found")
	}
	if view.Kind != KindMovie && view.Kind != KindTV {
		return fail("unknown kind " + view.Kind)
	}
	if view.Kind == KindMovie {
		src := largest(view.Item, view.Item.Files)
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
	}
	return out
}

func (t Tuning) Planned(view View) View { return t.Plan(view).View }

func Placements(item Item, ops []Op) []protocol.Placement {
	out := make([]protocol.Placement, 0, len(ops))
	for _, op := range ops {
		root, rel, _ := strings.Cut(op.Dst, "/")
		out = append(out, protocol.Placement{
			Source:      protocol.Path{Root: item.Source.Root, Rel: op.Src},
			Destination: protocol.Path{Root: strings.ToLower(root), Rel: rel},
		})
	}
	return out
}

type PartialError struct {
	Cause  error
	Placed []string
}

func (e *PartialError) Error() string { return e.Cause.Error() }

func (e *PartialError) Unwrap() error { return e.Cause }

func (t Tuning) Edited(item Item, shows []string, form map[string]string) View {
	view := t.Defaults(item, shows)
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
	if err := set.Tag(ctx, store, set, Scope{}, item.ID, TagIgnored, true); err != nil {
		return &PartialError{Cause: err}
	}
	return nil
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

func (t Tuning) wanted(f TorrentFile) bool {
	size := int64(-1)
	if f.Size != nil {
		size = *f.Size
	}
	return t.IsVideo(f.Name, size) || t.IsSubtitle(path.Base(f.Name))
}

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
	if slices.ContainsFunc(files, func(f TorrentFile) bool { return t.wanted(f) }) {
		var keep, skip []int
		for _, f := range files {
			switch {
			case t.wanted(f) && f.Priority == PrioritySkip:
				keep = append(keep, f.Index)
			case !t.wanted(f) && f.Priority != PrioritySkip:
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
