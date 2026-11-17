package protocol

import "time"

const (
	CodeScanChanged = "scan_changed"
	LibraryPageSize = 500
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
