package media

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"regexp"

	"github.com/jeiang/atrium/internal/mediaagent/protocol"
)

const (
	Movies = "movies"
	TV     = "tv"
)

func parseInt(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	if body == "" || strings.HasPrefix(body, "_") || strings.HasSuffix(body, "_") || strings.Contains(body, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(body, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func intOf(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case float64:
		if math.IsNaN(n) || math.IsInf(n, 0) || n < math.MinInt64 || n >= math.MaxInt64 {
			return 0, false
		}
		return int64(math.Trunc(n)), true
	}
	return 0, false
}

var (
	episodeRe = regexp.MustCompile(`(?i)\bS(\d{1,2}) ?E(\d{1,3})`)
	yearRe    = regexp.MustCompile(`\b(19\d{2}|20\d{2})\b`)
	leadTagRe = regexp.MustCompile(`^\s*\[[^\]]*\]\s*`)

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

func (t Tuning) IsVideo(rel string, size int64) bool {
	return t.VideoExts[strings.ToLower(Ext(rel))] && !protocol.Sample(rel, size)
}

func (t Tuning) IsSubtitle(name string) bool { return t.SubtitleExts[strings.ToLower(Ext(name))] }

func (t Tuning) ParseRelease(name string) Parsed {
	if t.VideoExts[strings.ToLower(Ext(name))] {
		name = name[:len(name)-len(Ext(name))]
	}
	name = leadTagRe.ReplaceAllString(name, "")
	name = strings.ReplaceAll(strings.ReplaceAll(name, ".", " "), "_", " ")
	var season, episode int64
	cut := len(name)
	if loc := episodeRe.FindStringSubmatchIndex(name); loc != nil {
		season, _ = parseInt(name[loc[2]:loc[3]])
		episode, _ = parseInt(name[loc[4]:loc[5]])
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
	text = strings.Join(strings.Fields(unsafe.Replace(text)), " ")
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
	year = strings.TrimSpace(year)
	if year == "" {
		return title, nil
	}
	if utf8.RuneCountInString(year) != 4 || !yearRe.MatchString(year) {
		return "", ParseError(fmt.Sprintf("year %q is not a year", year))
	}
	return fmt.Sprintf("%s (%s)", title, year), nil
}

func MoviePath(title, year, extension string) (string, error) {
	n, err := named(title, year)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s/%s%s", Movies, n, n, strings.ToLower(extension)), nil
}

func EpisodePath(show, year string, season, episode int, extension string) (string, error) {
	n, err := named(show, year)
	if err != nil {
		return "", err
	}
	if season < 0 || episode < 1 {
		return "", ParseError(fmt.Sprintf("season %d episode %d is not an episode", season, episode))
	}
	return fmt.Sprintf("%s/%s/Season %02d/%s - S%02dE%02d%s", TV, n, season, n, season, episode, strings.ToLower(extension)), nil
}
