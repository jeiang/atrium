package media

import (
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

var (
	seasonDir   = rx.MustCompile(`^Season (\d{1,3})$`, 0)
	episodeMark = rx.MustCompile(` - S(\d{2})E(\d{2,3})$`, 0)
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

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirsOf(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, e := range entries {
		if isDir(filepath.Join(path, e.Name())) {
			out = append(out, e.Name())
		}
	}
	return out
}

func (t Tuning) videosOf(path string) []string {
	entries, err := os.ReadDir(path)
	if err != nil {
		return []string{}
	}
	out := []string{}
	for _, e := range entries {
		if t.IsVideo(e.Name()) && isFile(filepath.Join(path, e.Name())) {
			out = append(out, e.Name())
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

func SafeRel(root, rel string) (string, bool) {
	if rel == "" || strings.HasPrefix(rel, "/") || strings.ContainsRune(rel, 0) {
		return "", false
	}
	var parts []string
	for part := range strings.SplitSeq(rel, "/") {
		if part != "" && part != "." {
			parts = append(parts, part)
		}
	}
	if len(parts) == 0 || (parts[0] != Movies && parts[0] != TV) || slices.Contains(parts, "..") {
		return "", false
	}
	full := filepath.Join(root, rel)
	if !isFile(full) {
		return "", false
	}
	return full, true
}

func (t Tuning) MovieEntries(root string) []Entry {
	dirs := dirsOf(filepath.Join(root, Movies))
	out := []Entry{}
	for _, d := range dirs[:min(t.ChildLimit, len(dirs))] {
		out = append(out, t.entryOf(Movies, d))
	}
	return out
}

func (t Tuning) ShowEntries(root string) []Entry {
	dirs := dirsOf(filepath.Join(root, TV))
	out := []Entry{}
	for _, d := range dirs[:min(t.ChildLimit, len(dirs))] {
		out = append(out, t.entryOf(TV, d))
	}
	return out
}

func fileOf(root, rel string) File {
	full := filepath.Join(root, rel)
	var size int64
	if info, err := os.Stat(full); err == nil {
		size = info.Size()
	}
	return File{Rel: rel, Name: filepath.Base(full), Size: size}
}

func badName(name string) bool { return strings.Contains(name, "/") || name == "." || name == ".." }

func (t Tuning) Movie(root, name string) (MovieView, error) {
	path := filepath.Join(root, Movies, name)
	if badName(name) || !isDir(path) {
		return MovieView{}, NotFound(name)
	}
	view := MovieView{Entry: t.entryOf(Movies, name), Files: []File{}}
	for _, f := range t.videosOf(path) {
		view.Files = append(view.Files, fileOf(root, Movies+"/"+name+"/"+f))
	}
	return view, nil
}

func episodeNumber(name string) (*int, *int) {
	m := matched(episodeMark.Search(name[:len(name)-len(Ext(name))]))
	if m == nil {
		return nil, nil
	}
	season, errSeason := py.IntString(m.Group(1))
	episode, errEpisode := py.IntString(m.Group(2))
	if errSeason != nil || errEpisode != nil {
		return nil, nil
	}
	s, e := int(season.Int64()), int(episode.Int64())
	return &s, &e
}

func (t Tuning) Show(root, name string) (ShowView, error) {
	path := filepath.Join(root, TV, name)
	if badName(name) || !isDir(path) {
		return ShowView{}, NotFound(name)
	}
	view := ShowView{Entry: t.entryOf(TV, name), Seasons: []Season{}}
	for _, d := range dirsOf(path) {
		season := Season{Dir: d, Episodes: []Episode{}}
		if m := matched(seasonDir.Match(d)); m != nil {
			if n, err := py.IntString(m.Group(1)); err == nil {
				v := int(n.Int64())
				season.Number = &v
			}
		}
		for _, f := range t.videosOf(filepath.Join(path, d)) {
			s, e := episodeNumber(f)
			season.Episodes = append(season.Episodes, Episode{File: fileOf(root, TV+"/"+name+"/"+d+"/"+f), Season: s, Episode: e})
		}
		view.Seasons = append(view.Seasons, season)
	}
	return view, nil
}

func (t Tuning) EpisodeFilesOf(root, name string) []string {
	tv := filepath.Join(root, TV, name)
	out := []string{}
	for _, d := range dirsOf(tv) {
		for _, f := range t.videosOf(filepath.Join(tv, d)) {
			out = append(out, TV+"/"+name+"/"+d+"/"+f)
		}
	}
	return out
}

func (t Tuning) EpisodeFiles(root string) []string {
	out := []string{}
	for _, name := range dirsOf(filepath.Join(root, TV)) {
		out = append(out, t.EpisodeFilesOf(root, name)...)
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

func (t Tuning) Search(root, query string, limit int) []SearchResult {
	var words []string
	for _, w := range py.Split(query) {
		words = append(words, py.Casefold(w))
	}
	out := []SearchResult{}
	if len(words) == 0 {
		return out
	}
	for _, m := range t.MovieEntries(root) {
		if !containsAll(py.Casefold(m.Dir), words) {
			continue
		}
		view, _ := t.Movie(root, m.Dir)
		files := []string{}
		for _, f := range view.Files {
			files = append(files, f.Rel)
		}
		out = append(out, SearchResult{Kind: KindMovie, Title: m.Title, Year: m.Year, Dir: m.Dir, Files: files})
	}
	for _, s := range t.ShowEntries(root) {
		for _, rel := range t.EpisodeFilesOf(root, s.Dir) {
			if containsAll(py.Casefold(s.Dir+" "+filepath.Base(rel)), words) {
				out = append(out, SearchResult{Kind: "episode", Title: s.Title, Year: s.Year, Dir: s.Dir, Files: []string{rel}})
			}
		}
		if len(out) >= limit {
			break
		}
	}
	return out[:min(limit, len(out))]
}
