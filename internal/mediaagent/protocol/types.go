package protocol

const Version = 1

const (
	RootMovies    = "movies"
	RootTV        = "tv"
	RootDownloads = "downloads"
	RootImport    = "import"
)

const (
	CapabilityFile      = "file"
	CapabilityScan      = "scan"
	CapabilityThumbnail = "thumbnail"
	CapabilityStream    = "stream"
)

type Path struct {
	Root string `json:"root"`
	Rel  string `json:"rel"`
}

type Identity struct {
	Device  uint64 `json:"device"`
	Inode   uint64 `json:"inode"`
	Size    int64  `json:"size"`
	MTimeNS int64  `json:"mtime_ns"`
}

type Health struct {
	Protocol     int      `json:"protocol"`
	AgentID      string   `json:"agent_id"`
	RootRevision string   `json:"root_revision"`
	Capabilities []string `json:"capabilities"`
	Ready        bool     `json:"ready"`
	ReasonCode   string   `json:"reason_code"`
}
