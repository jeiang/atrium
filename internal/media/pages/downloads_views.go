package pages

import (
	mediaapp "github.com/jeiang/atrium/internal/media/application"
)

const dlDownloadsPage = "/apps/media/downloads"

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

type downloadsView struct {
	Queue      []QueueView `json:"queue"`
	QueueError string      `json:"queue_error"`
	Kinds      []string    `json:"kinds"`
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
