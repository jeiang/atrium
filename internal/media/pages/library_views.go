package pages

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
	Movie    MovieView `json:"movie"`
	TTLHours int64     `json:"ttl_hours"`
}

type ShowPage struct {
	Show     ShowView `json:"show"`
	TTLHours int64    `json:"ttl_hours"`
}
