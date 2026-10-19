package pages

type ChannelView struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	IMDb          string `json:"imdb"`
	Pattern       string `json:"pattern"`
	CaseSensitive bool   `json:"case_sensitive"`
	Primed        bool   `json:"primed"`
}

type ChannelStatusView struct {
	LastCheck *float64 `json:"last_check"`
	Errors    []string `json:"errors"`
}

type SearchHit struct {
	TmdbID int64  `json:"tmdb_id"`
	Name   string `json:"name"`
	Year   string `json:"year"`
}

type ChannelForm struct {
	TmdbID        string `json:"tmdb_id"`
	Name          string `json:"name"`
	IMDb          string `json:"imdb"`
	Pattern       string `json:"pattern"`
	CaseSensitive bool   `json:"case_sensitive"`
	Backfill      bool   `json:"backfill"`
}

type ChannelsPage struct {
	Channels []ChannelView     `json:"channels"`
	Status   ChannelStatusView `json:"status"`
	TMDB     bool              `json:"tmdb"`
	Q        string            `json:"q"`
	Results  *[]SearchHit      `json:"results"`
	Form     ChannelForm       `json:"form"`
}

type ReleaseView struct {
	Title     string `json:"title"`
	Match     bool   `json:"match"`
	SizeBytes int64  `json:"size_bytes"`
}

type PreviewView struct {
	Error    string         `json:"error"`
	Matched  int            `json:"matched"`
	Releases *[]ReleaseView `json:"releases"`
}
