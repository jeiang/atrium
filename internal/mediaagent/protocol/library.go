package protocol

import "time"

const (
	CodeScanChanged    = "scan_changed"
	MaxThumbnailPaths  = 1000
	LibraryPageSize    = 500
	ThumbnailMediaType = "image/jpeg"
)

type Node struct {
	Root     string   `json:"root"`
	Rel      string   `json:"rel"`
	Dir      bool     `json:"dir"`
	Size     int64    `json:"size"`
	Identity Identity `json:"identity"`
}

type LibraryPage struct {
	SnapshotID   string    `json:"snapshot_id"`
	ScannedAt    time.Time `json:"scanned_at"`
	RootRevision string    `json:"root_revision"`
	Nodes        []Node    `json:"nodes"`
	Next         string    `json:"next"`
}

type ThumbnailPass struct {
	Paths []Path `json:"paths"`
	Limit int    `json:"limit"`
}

type ThumbnailResult struct {
	Generated int `json:"generated"`
	Remaining int `json:"remaining"`
	Failed    int `json:"failed"`
}
