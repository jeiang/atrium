package media

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/rx"
)

const (
	Movies = "movies"
	TV     = "tv"
)

var (
	episodeRe = rx.MustCompile(`\bS(\d{1,2}) ?E(\d{1,3})`, rx.IgnoreCase|rx.ASCII)
	yearRe    = rx.MustCompile(`\b(19\d{2}|20\d{2})\b`, rx.ASCII)
	leadTagRe = rx.MustCompile(`^\s*\[[^\]]*\]\s*`, rx.ASCII)
	sampleRe  = rx.MustCompile(`\bsample\b`, rx.IgnoreCase|rx.ASCII)

	unsafe = strings.NewReplacer("/", "", "\\", "", ":", " -", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")

	videoExts    = map[string]bool{".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true, ".ts": true, ".wmv": true}
	subtitleExts = map[string]bool{".srt": true, ".sub": true, ".idx": true, ".ass": true, ".ssa": true, ".vtt": true}
)

type ParseError string

func (e ParseError) Error() string { return string(e) }

type Parsed struct {
	Title   string
	Year    string
	Season  int
	Episode int
}

func matched(m *rx.Match, err error) *rx.Match {
	if err != nil {
		return nil
	}
	return m
}

func Ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

func IsVideo(name string) bool {
	return videoExts[py.Lower(Ext(name))] && matched(sampleRe.Search(name)) == nil
}

func IsSubtitle(name string) bool { return subtitleExts[py.Lower(Ext(name))] }

func ParseRelease(name string) Parsed {
	if videoExts[py.Lower(Ext(name))] {
		name = name[:len(name)-len(Ext(name))]
	}
	if stripped, err := leadTagRe.Sub("", name, 1); err == nil {
		name = stripped
	}
	name = strings.ReplaceAll(strings.ReplaceAll(name, ".", " "), "_", " ")
	runes := []rune(name)
	var season, episode int64
	cut := len(runes)
	if m := matched(episodeRe.Search(name)); m != nil {
		season, _ = py.ParseInt(m.Group(1))
		episode, _ = py.ParseInt(m.Group(2))
		cut = m.Start()
	}
	year := ""
	years, _ := yearRe.FindIterRange(name, 0, cut)
	for _, m := range years {
		if m.Start() > 0 {
			year, cut = m.Group(1), m.Start()
		}
	}
	return Parsed{Title: strings.Trim(string(runes[:cut]), " -(["), Year: year, Season: int(season), Episode: int(episode)}
}

func cleanComponent(text string) (string, error) {
	text = strings.Join(py.Split(unsafe.Replace(text)), " ")
	text = strings.Trim(text, " .")
	if text == "" {
		return "", ParseError("title is empty")
	}
	return text, nil
}

func named(title, year string) (string, error) {
	title, err := cleanComponent(title)
	if err != nil {
		return "", err
	}
	year = py.Strip(year)
	if year == "" {
		return title, nil
	}
	if utf8.RuneCountInString(year) != 4 || matched(yearRe.Search(year)) == nil {
		return "", ParseError(fmt.Sprintf("year %s is not a year", py.Repr(year)))
	}
	return fmt.Sprintf("%s (%s)", title, year), nil
}

func MoviePath(title, year, extension string) (string, error) {
	n, err := named(title, year)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s%s", Movies, n, n, py.Lower(extension)), nil
}

func EpisodePath(show, year string, season, episode int, extension string) (string, error) {
	n, err := named(show, year)
	if err != nil {
		return "", err
	}
	if season < 0 || episode < 1 {
		return "", ParseError(fmt.Sprintf("season %d episode %d is not an episode", season, episode))
	}
	return fmt.Sprintf("%s/%s/Season %02d/%s - S%02dE%02d%s", TV, n, season, n, season, episode, py.Lower(extension)), nil
}
