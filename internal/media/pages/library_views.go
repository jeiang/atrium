package pages

import "strconv"

type LibraryEntry struct {
	Kind  string `json:"kind"`
	Dir   string `json:"dir"`
	Title string `json:"title"`
	Year  string `json:"year"`
}

type LibraryPage struct {
	Error  string         `json:"error"`
	Movies []LibraryEntry `json:"movies"`
	Shows  []LibraryEntry `json:"shows"`
}

type FileLink struct {
	Rel    string `json:"rel"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Public string `json:"public"`
	VPN    string `json:"vpn"`
}

type EpisodeView struct {
	Rel     string `json:"rel"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Public  string `json:"public"`
	VPN     string `json:"vpn"`
	Season  *int64 `json:"season"`
	Episode *int64 `json:"episode"`
}

type SeasonView struct {
	Number   *int64        `json:"number"`
	Dir      string        `json:"dir"`
	Episodes []EpisodeView `json:"episodes"`
}

type ShowView struct {
	Dir     string       `json:"dir"`
	Title   string       `json:"title"`
	Year    string       `json:"year"`
	Seasons []SeasonView `json:"seasons"`
}

type MovieView struct {
	Dir   string     `json:"dir"`
	Title string     `json:"title"`
	Year  string     `json:"year"`
	Files []FileLink `json:"files"`
}

type MoviePage struct {
	Movie MovieView `json:"movie"`
	TTL   string    `json:"ttl"`
}

type ShowPage struct {
	Show ShowView `json:"show"`
	TTL  string   `json:"ttl"`
}

func linkSpan(seconds int64) string {
	unit, size := "second", int64(1)
	switch {
	case seconds%86400 == 0:
		unit, size = "day", 86400
	case seconds%3600 == 0:
		unit, size = "hour", 3600
	case seconds%60 == 0:
		unit, size = "minute", 60
	}
	n := seconds / size
	if n == 1 {
		return "1 " + unit
	}
	return strconv.FormatInt(n, 10) + " " + unit + "s"
}
