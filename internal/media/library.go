package media

import (
	"path"
	"regexp"
	"strings"

	"github.com/jeiang/atrium/internal/textutil"
)

var (
	seasonDir   = regexp.MustCompile(`^Season (\d{1,3})$`)
	episodeMark = regexp.MustCompile(` - S(\d{2})E(\d{2,3})$`)
)

type NotFound string

func (e NotFound) Error() string { return string(e) }

type Entry struct {
	Dir   string
	Title string
	Year  string
	Kind  string
}

type File struct {
	Rel  string
	Name string
	Size int64
}

type Episode struct {
	File
	Season  *int
	Episode *int
}

type Season struct {
	Dir      string
	Number   *int
	Episodes []Episode
}

type MovieView struct {
	Entry
	Files []File
}

type ShowView struct {
	Entry
	Seasons []Season
}

type SearchResult struct {
	Kind  string
	Title string
	Year  string
	Dir   string
	Files []string
}

func dirsOf(snap Snapshot, root, parent string) []string {
	out := []string{}
	for _, n := range snap.list(root, parent) {
		if n.Dir {
			out = append(out, path.Base(n.Rel))
		}
	}
	return out
}

func (t Tuning) videosOf(snap Snapshot, root, parent string) []File {
	out := []File{}
	for _, n := range snap.list(root, parent) {
		if name := path.Base(n.Rel); !n.Dir && t.IsVideo(name) {
			out = append(out, File{Rel: root + "/" + n.Rel, Name: name, Size: n.Size})
		}
	}
	return out
}

func (t Tuning) entryOf(kind, name string) Entry {
	r := t.ParseRelease(name)
	title := r.Title
	if title == "" {
		title = name
	}
	return Entry{Dir: name, Title: title, Year: r.Year, Kind: kind}
}

func (t Tuning) MovieEntries(snap Snapshot) []Entry {
	dirs := dirsOf(snap, Movies, "")
	out := []Entry{}
	for _, d := range dirs[:min(t.ChildLimit, len(dirs))] {
		out = append(out, t.entryOf(KindMovie, d))
	}
	return out
}

func (t Tuning) ShowEntries(snap Snapshot) []Entry {
	dirs := dirsOf(snap, TV, "")
	out := []Entry{}
	for _, d := range dirs[:min(t.ChildLimit, len(dirs))] {
		out = append(out, t.entryOf(KindShow, d))
	}
	return out
}

func badName(name string) bool {
	return name == "" || strings.Contains(name, "/") || name == "." || name == ".."
}

func isDirIn(snap Snapshot, root, name string) bool {
	n, ok := snap.node(root, name)
	return ok && n.Dir
}

func (t Tuning) Movie(snap Snapshot, name string) (MovieView, error) {
	if badName(name) || !isDirIn(snap, Movies, name) {
		return MovieView{}, NotFound(name)
	}
	return MovieView{Entry: t.entryOf(Movies, name), Files: t.videosOf(snap, Movies, name)}, nil
}

func episodeNumber(name string) (*int, *int) {
	text := name[:len(name)-len(Ext(name))]
	loc := episodeMark.FindStringSubmatchIndex(text)
	if loc == nil {
		return nil, nil
	}
	season, okSeason := parseInt(text[loc[2]:loc[3]])
	episode, okEpisode := parseInt(text[loc[4]:loc[5]])
	if !okSeason || !okEpisode {
		return nil, nil
	}
	s, e := int(season), int(episode)
	return &s, &e
}

func (t Tuning) Show(snap Snapshot, name string) (ShowView, error) {
	if badName(name) || !isDirIn(snap, TV, name) {
		return ShowView{}, NotFound(name)
	}
	view := ShowView{Entry: t.entryOf(TV, name), Seasons: []Season{}}
	for _, d := range dirsOf(snap, TV, name) {
		season := Season{Dir: d, Episodes: []Episode{}}
		if loc := seasonDir.FindStringSubmatchIndex(d); loc != nil && loc[0] == 0 {
			if n, ok := parseInt(d[loc[2]:loc[3]]); ok {
				v := int(n)
				season.Number = &v
			}
		}
		for _, f := range t.videosOf(snap, TV, name+"/"+d) {
			s, e := episodeNumber(f.Name)
			season.Episodes = append(season.Episodes, Episode{File: f, Season: s, Episode: e})
		}
		view.Seasons = append(view.Seasons, season)
	}
	return view, nil
}

func (t Tuning) EpisodeFilesOf(snap Snapshot, name string) []string {
	out := []string{}
	for _, d := range dirsOf(snap, TV, name) {
		for _, f := range t.videosOf(snap, TV, name+"/"+d) {
			out = append(out, f.Rel)
		}
	}
	return out
}

func (t Tuning) EpisodeFiles(snap Snapshot) []string {
	out := []string{}
	for _, name := range dirsOf(snap, TV, "") {
		out = append(out, t.EpisodeFilesOf(snap, name)...)
	}
	return out
}
func containsAll(hay string, words []string) bool {
	for _, w := range words {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func (t Tuning) Search(snap Snapshot, query string, limit int) []SearchResult {
	var words []string
	for _, w := range strings.Fields(query) {
		words = append(words, textutil.Casefold(w))
	}
	out := []SearchResult{}
	if len(words) == 0 {
		return out
	}
	for _, m := range t.MovieEntries(snap) {
		if !containsAll(textutil.Casefold(m.Dir), words) {
			continue
		}
		view, _ := t.Movie(snap, m.Dir)
		files := []string{}
		for _, f := range view.Files {
			files = append(files, f.Rel)
		}
		out = append(out, SearchResult{Kind: KindMovie, Title: m.Title, Year: m.Year, Dir: m.Dir, Files: files})
		if len(out) >= limit {
			return out[:limit]
		}
	}
	for _, s := range t.ShowEntries(snap) {
		for _, rel := range t.EpisodeFilesOf(snap, s.Dir) {
			if containsAll(textutil.Casefold(s.Dir+" "+path.Base(rel)), words) {
				out = append(out, SearchResult{Kind: KindEpisode, Title: s.Title, Year: s.Year, Dir: s.Dir, Files: []string{rel}})
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out[:min(limit, len(out))]
}
