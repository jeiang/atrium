package library

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/apps/library/agentclient"
	"github.com/jeiang/atrium/internal/apps/library/mediaagent/protocol"
	"github.com/jeiang/atrium/internal/apps/torrents/contract"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/textutil"
)

const (
	Skipped    = "(skipped)"
	importPref = Import + "/"

	TagMedia    = contract.TagLegacy
	TagFiled    = "filed"
	TagIgnored  = "ignored"
	TagSelected = "selected"
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

func (t Tuning) Items(ctx context.Context, in contract.Intake, agent *agentclient.Client, prefix string) ([]Item, string, error) {
	found := []Item{}
	var problems []string
	torrents, err := in.List(ctx, contract.Filter{Completed: true})
	if err != nil {
		if !contract.IsQbit(err) {
			return nil, "", err
		}
		torrents, problems = nil, append(problems, err.Error())
	}
	for _, torrent := range torrents {
		if !Waiting(torrent) {
			continue
		}
		found = append(found, t.TorrentItem(ctx, agent, prefix, torrent))
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

func Waiting(torrent contract.Torrent) bool {
	return torrent.HasTag(TagMedia) && !torrent.HasTag(TagFiled) && !torrent.HasTag(TagIgnored)
}

func (t Tuning) TorrentItem(ctx context.Context, agent *agentclient.Client, prefix string, torrent contract.Torrent) Item {
	item := Item{ID: torrent.Hash, Name: torrent.Name, Torrent: true}
	rel, ok := DownloadRel(prefix, torrent.ContentPath)
	if !ok {
		item.Error = "the download is outside " + KeyDownloadPrefix
		return item
	}
	item.Source = protocol.Path{Root: protocol.RootDownloads, Rel: rel}
	if err := t.fill(ctx, agent, &item); err != nil {
		item.Error = agentText(err)
	}
	return item
}

func (t Tuning) Find(ctx context.Context, in contract.Intake, agent *agentclient.Client, prefix, itemID string) (Item, error) {
	found, errText, err := t.Items(ctx, in, agent, prefix)
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
	return Item{}, NotFound(itemID + " is no longer waiting")
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

func Ignore(ctx context.Context, set Settings, item Item) ([]int64, error) {
	if !item.Torrent {
		return nil, PlanError("Only torrents can be ignored; delete the import entry instead.")
	}
	scope := contract.Scope{Actor: contract.Operator}
	var effects []int64
	unfiled, err := set.Intake.Mark(ctx, scope, item.ID, TagMedia, TagFiled, false)
	if unfiled != 0 {
		effects = append(effects, unfiled)
	}
	if err != nil {
		return effects, err
	}
	ignored, err := set.Intake.Mark(ctx, scope, item.ID, TagMedia, TagIgnored, true)
	if ignored != 0 {
		effects = append(effects, ignored)
	}
	if err != nil {
		return effects, &PartialError{Cause: err}
	}
	return effects, nil
}

type SweepResult struct {
	Removed []string
	Errors  []string
}

func soft(err error) bool {
	var conflict *ledger.Conflict
	var refused *contract.Refused
	return contract.IsQbit(err) || errors.As(err, &conflict) || errors.As(err, &refused)
}

func Sweep(ctx context.Context, set Settings) (SweepResult, error) {
	out := SweepResult{Removed: []string{}, Errors: []string{}}
	if err := set.Intake.Ready(ctx); err != nil {
		return out, err
	}
	torrents, err := set.Intake.List(ctx, contract.Filter{Completed: true, Label: TagMedia})
	if err != nil {
		if !contract.IsQbit(err) {
			return out, err
		}
		out.Errors = append(out.Errors, err.Error())
		return out, nil
	}
	for _, t := range torrents {
		if !t.HasTag(TagMedia) || !t.HasTag(TagFiled) || t.State != contract.StoppedUP {
			continue
		}
		if _, err := set.Intake.Control(ctx, contract.Control{Scope: contract.Scope{Actor: contract.Job}, Hash: t.Hash, Label: TagMedia, Action: contract.Remove, DeleteFiles: true}); err != nil {
			if !soft(err) {
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

func (t Tuning) wanted(f contract.TorrentFile) bool {
	size := int64(-1)
	if f.Size != nil {
		size = *f.Size
	}
	return t.IsVideo(f.Name, size) || t.IsSubtitle(path.Base(f.Name))
}

func (t Tuning) SelectFiles(ctx context.Context, set Settings, scope contract.Scope, torrents []contract.Torrent) ([]string, error) {
	done := []string{}
	if torrents == nil {
		var err error
		torrents, err = set.Intake.List(ctx, contract.Filter{Label: TagMedia})
		if err != nil {
			if contract.IsQbit(err) {
				return done, nil
			}
			return done, err
		}
	}
	for _, torrent := range torrents {
		if !torrent.HasTag(TagMedia) || torrent.HasTag(TagSelected) {
			continue
		}
		selected, err := t.selectOne(ctx, set, scope, torrent)
		if err != nil {
			if !soft(err) {
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

func (t Tuning) selectOne(ctx context.Context, set Settings, scope contract.Scope, torrent contract.Torrent) (bool, error) {
	files, err := set.Intake.Files(ctx, torrent.Hash)
	if err != nil || len(files) == 0 {
		return false, err
	}
	if slices.ContainsFunc(files, func(f contract.TorrentFile) bool { return t.wanted(f) }) {
		var keep, skip []int
		for _, f := range files {
			switch {
			case t.wanted(f) && f.Priority == contract.PrioritySkip:
				keep = append(keep, f.Index)
			case !t.wanted(f) && f.Priority != contract.PrioritySkip:
				skip = append(skip, f.Index)
			}
		}
		if len(keep) > 0 {
			if err := set.Intake.SetWanted(ctx, scope, torrent.Hash, TagMedia, keep, true); err != nil {
				return false, err
			}
		}
		if len(skip) > 0 {
			if err := set.Intake.SetWanted(ctx, scope, torrent.Hash, TagMedia, skip, false); err != nil {
				return false, err
			}
		}
	}
	if _, err := set.Intake.Mark(ctx, scope, torrent.Hash, TagMedia, TagSelected, true); err != nil {
		return false, err
	}
	if torrent.State == contract.StoppedDL || torrent.State == "pausedDL" {
		if _, err := set.Intake.Control(ctx, contract.Control{Scope: scope, Hash: torrent.Hash, Label: TagMedia, Action: contract.Resume}); err != nil {
			return false, err
		}
	}
	return true, nil
}
