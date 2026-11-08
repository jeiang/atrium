package media

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"regexp"

	"github.com/jeiang/atrium/internal/py"
)

const (
	Movies = "movies"
	TV     = "tv"
)

var (
	episodeRe = regexp.MustCompile(`(?i)\bS(\d{1,2}) ?E(\d{1,3})`)
	yearRe    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	leadTagRe = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)
	sampleRe  = regexp.MustCompile(`(?i)\bsample\b`)

	unsafe = strings.NewReplacer("/", "", "\\", "", ":", " -", "*", "", "?", "", `"`, "", "<", "", ">", "", "|", "")
)

type ParseError string

func (e ParseError) Error() string { return string(e) }

type Parsed struct {
	Title   string
	Year    string
	Season  int
	Episode int
}

func Ext(name string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	return ""
}

func (t Tuning) IsVideo(name string) bool {
	return t.VideoExts[py.Lower(Ext(name))] && !sampleRe.MatchString(name)
}

func (t Tuning) IsSubtitle(name string) bool { return t.SubtitleExts[py.Lower(Ext(name))] }

func (t Tuning) ParseRelease(name string) Parsed {
	if t.VideoExts[py.Lower(Ext(name))] {
		name = name[:len(name)-len(Ext(name))]
	}
	name = leadTagRe.ReplaceAllString(name, "")
	name = strings.ReplaceAll(strings.ReplaceAll(name, ".", " "), "_", " ")
	var season, episode int64
	cut := len(name)
	if loc := episodeRe.FindStringSubmatchIndex(name); loc != nil {
		season, _ = py.ParseInt(name[loc[2]:loc[3]])
		episode, _ = py.ParseInt(name[loc[4]:loc[5]])
		cut = loc[0]
	}
	year := ""
	for _, loc := range yearRe.FindAllStringSubmatchIndex(name[:cut], -1) {
		if loc[0] > 0 {
			year, cut = name[loc[2]:loc[3]], loc[0]
		}
	}
	return Parsed{Title: strings.Trim(name[:cut], " -(["), Year: year, Season: int(season), Episode: int(episode)}
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
	if utf8.RuneCountInString(year) != 4 || !yearRe.MatchString(year) {
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
