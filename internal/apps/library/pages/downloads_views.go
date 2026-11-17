package pages

import (
	"fmt"
	"math"
	"strconv"

	mediaapp "github.com/jeiang/atrium/internal/apps/library/application"
)

const (
	dlModeMedia     = "media"
	dlModePlain     = "plain"
	dlPollSeconds   = 5
	dlInfiniteETA   = 8640000
	dlDownloadsPage = "/apps/media/downloads"
)

type TorrentView struct {
	Hash    string `json:"hash"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Size    string `json:"size"`
	Down    string `json:"down"`
	Up      string `json:"up"`
	ETA     string `json:"eta"`
	Percent int64  `json:"percent"`
	Filed   bool   `json:"filed"`
	Ignored bool   `json:"ignored"`
	Active  bool   `json:"active"`
}

type QueueFile struct {
	Rel     string `json:"rel"`
	Season  string `json:"season"`
	Episode string `json:"episode"`
	Dest    string `json:"dest"`
}

type QueueItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Torrent bool   `json:"torrent"`
}

type QueueView struct {
	Item      QueueItem   `json:"item"`
	Kind      string      `json:"kind"`
	Title     string      `json:"title"`
	Year      string      `json:"year"`
	Files     []QueueFile `json:"files"`
	MovieFile string      `json:"movie_file"`
	MovieDest string      `json:"movie_dest"`
	Error     string      `json:"error"`
}

type torrentsView struct {
	Torrents []TorrentView `json:"torrents"`
	Error    string        `json:"error"`
	Active   bool          `json:"active"`
	Poll     int           `json:"poll"`
}

type downloadsView struct {
	Torrents   []TorrentView `json:"torrents"`
	Error      string        `json:"error"`
	Active     bool          `json:"active"`
	Poll       int           `json:"poll"`
	Queue      []QueueView   `json:"queue"`
	QueueError string        `json:"queue_error"`
	Kinds      []string      `json:"kinds"`
}

func dlHumanSize(n *int64) string {
	size := 0.0
	if n != nil {
		size = float64(*n)
	}
	return dlHumanSizeFloat(size)
}

func dlHumanSizeFloat(n float64) string {
	for _, unit := range []string{"B", "KB", "MB", "GB"} {
		if n < 1024 || unit == "GB" {
			if unit == "B" {
				return strconv.FormatFloat(n, 'f', 0, 64) + " " + unit
			}
			return strconv.FormatFloat(n, 'f', 1, 64) + " " + unit
		}
		n /= 1024
	}
	return strconv.FormatFloat(n, 'f', 1, 64) + " GB"
}

func dlHumanETA(seconds *int64) string {
	if seconds == nil || *seconds <= 0 || *seconds >= dlInfiniteETA {
		return ""
	}
	s := *seconds
	switch {
	case s < 60:
		return strconv.FormatInt(s, 10) + "s"
	case s < 3600:
		return strconv.FormatInt(s/60, 10) + "m"
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s%3600/60)
}

func dlSpeedText(n *int64) string {
	if n == nil || *n == 0 {
		return ""
	}
	return dlHumanSize(n) + "/s"
}

func torrentView(t mediaapp.Torrent) TorrentView {
	state := t.State
	if state == "" {
		state = "unknown"
	}
	label, ok := mediaapp.StateLabels[state]
	if !ok {
		label = state
	}
	kind := dlModePlain
	if t.HasTag(mediaapp.TagMedia) {
		kind = dlModeMedia
	}
	return TorrentView{
		Hash: t.Hash, Name: t.Name, Kind: kind, Label: label, Size: dlHumanSize(t.Size),
		Down: dlSpeedText(t.DLSpeed), Up: dlSpeedText(t.UpSpeed), ETA: dlHumanETA(t.ETA),
		Percent: int64(math.Round(t.Progress * 100)),
		Filed:   t.HasTag(mediaapp.TagFiled), Ignored: t.HasTag(mediaapp.TagIgnored),
		Active: t.Active(),
	}
}

func queueView(v mediaapp.View) QueueView {
	files := make([]QueueFile, len(v.Files))
	for i, f := range v.Files {
		files[i] = QueueFile{Rel: f.Rel, Season: f.Season, Episode: f.Episode, Dest: f.Dest}
	}
	problem := v.Error
	if problem == "" {
		problem = v.Item.Error
	}
	return QueueView{
		Item: QueueItem{ID: v.Item.ID, Name: v.Item.Name, Torrent: v.Item.Torrent}, Kind: v.Kind, Title: v.Title,
		Year: v.Year, Files: files, MovieFile: v.MovieFile, MovieDest: v.MovieDest, Error: problem,
	}
}
