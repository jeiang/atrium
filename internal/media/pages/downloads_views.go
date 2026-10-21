package pages

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/media"
	"github.com/jeiang/atrium/internal/py"
)

const (
	dlModeMedia     = "media"
	dlModePlain     = "plain"
	dlPollSeconds   = 5
	dlInfiniteETA   = 8640000
	dlDownloadsPage = "/media/downloads"
)

var dlAddModes = []string{dlModeMedia, dlModePlain}

var dlActiveStates = []string{
	"downloading", "metaDL", "forcedMetaDL", "stalledDL", "checkingDL", "forcedDL", "queuedDL", "allocating",
	"moving", "checkingResumeData", "uploading", "forcedUP", "checkingUP",
}

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

type FileView struct {
	Index    int    `json:"index"`
	Wanted   bool   `json:"wanted"`
	Name     string `json:"name"`
	SizeText string `json:"size_text"`
	Percent  int64  `json:"percent"`
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

type filesView struct {
	Hash        string     `json:"hash"`
	TorrentName string     `json:"torrent_name"`
	Files       []FileView `json:"files"`
	Error       string     `json:"error"`
}

type addView struct {
	Magnet      string   `json:"magnet"`
	TorrentName string   `json:"torrent_name"`
	AddMode     string   `json:"add_mode"`
	Error       string   `json:"error"`
	Modes       []string `json:"modes"`
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
				return py.FormatFixed(n, 0) + " " + unit
			}
			return py.FormatFixed(n, 1) + " " + unit
		}
		n /= 1024
	}
	return py.FormatFixed(n, 1) + " GB"
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

func torrentView(t media.Torrent) TorrentView {
	state := t.State
	if state == "" {
		state = "unknown"
	}
	label, ok := media.StateLabels[state]
	if !ok {
		label = state
	}
	kind := dlModePlain
	if t.HasTag(media.TagMedia) {
		kind = dlModeMedia
	}
	return TorrentView{
		Hash: t.Hash, Name: t.Name, Kind: kind, Label: label, Size: dlHumanSize(t.Size),
		Down: dlSpeedText(t.DLSpeed), Up: dlSpeedText(t.UpSpeed), ETA: dlHumanETA(t.ETA),
		Percent: int64(py.Round(t.Progress*100, 0)),
		Filed:   t.HasTag(media.TagFiled), Ignored: t.HasTag(media.TagIgnored),
		Active: slices.Contains(dlActiveStates, state),
	}
}

func fileView(f media.TorrentFile) FileView {
	return FileView{
		Index: f.Index, Wanted: f.Priority != media.PrioritySkip, Name: f.Name,
		SizeText: dlHumanSize(f.Size), Percent: int64(py.Round(f.Progress*100, 0)),
	}
}

func queueView(v media.View) QueueView {
	files := make([]QueueFile, len(v.Files))
	for i, f := range v.Files {
		files[i] = QueueFile{Rel: f.Rel, Season: f.Season, Episode: f.Episode, Dest: f.Dest}
	}
	return QueueView{
		Item: QueueItem{ID: v.Item.ID, Name: v.Item.Name, Torrent: v.Item.Torrent}, Kind: v.Kind, Title: v.Title,
		Year: v.Year, Files: files, MovieFile: v.MovieFile, MovieDest: v.MovieDest, Error: v.Error,
	}
}

func dlDecodeTorrent(b64 string, limit int) (data []byte, present bool, err error) {
	b64 = strings.Join(py.Split(b64), "")
	if b64 == "" {
		return nil, false, nil
	}
	n := utf8.RuneCountInString(b64)
	if n > limit {
		return nil, false, dlTorrentError("the torrent file is too large")
	}
	b64 = strings.NewReplacer("-", "+", "_", "/").Replace(b64)
	b64 += strings.Repeat("=", (4-n%4)%4)
	data, err = py.B64Decode(b64)
	if err != nil {
		return nil, false, dlTorrentError("the torrent data is not base64")
	}
	return data, true, nil
}

type dlTorrentError string

func (e dlTorrentError) Error() string { return string(e) }
