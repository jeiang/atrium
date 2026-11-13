package protocol

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
)

const (
	StepMkdir           = "mkdir"
	StepLink            = "link"
	StepRename          = "rename"
	StepUnlink          = "unlink"
	StepRmdir           = "rmdir"
	SourceTorrent       = "torrent"
	SourceImport        = "import"
	SourceImportCleanup = "import_cleanup"
)

type File struct {
	Path     Path     `json:"path"`
	Identity Identity `json:"identity"`
}
type Step struct {
	Index       int       `json:"index"`
	Kind        string    `json:"kind"`
	Source      *File     `json:"source,omitempty"`
	Destination Path      `json:"destination"`
	Expect      *Identity `json:"expect,omitempty"`
}
type FilingPlan struct {
	Version      int    `json:"version"`
	AgentID      string `json:"agent_id"`
	RootRevision string `json:"root_revision"`
	SourceKind   string `json:"source_kind"`
	TorrentHash  string `json:"torrent_hash"`
	Steps        []Step `json:"steps"`
}
type Placement struct {
	Source      Path `json:"source"`
	Destination Path `json:"destination"`
}
type PrepareRequest struct {
	SourceKind  string      `json:"source_kind"`
	TorrentHash string      `json:"torrent_hash"`
	Source      Path        `json:"source"`
	Placements  []Placement `json:"placements"`
	Cleanup     bool        `json:"cleanup"`
}
type PrepareResponse struct {
	Plan       FilingPlan `json:"plan"`
	PlanDigest string     `json:"plan_digest"`
}
type FilingRequest struct {
	Key        string     `json:"key"`
	PlanDigest string     `json:"plan_digest"`
	Plan       FilingPlan `json:"plan"`
	UndoOf     string     `json:"undo_of,omitempty"`
}
type StepResult struct {
	Index     int       `json:"index"`
	State     string    `json:"state"`
	Before    *Identity `json:"before,omitempty"`
	After     *Identity `json:"after,omitempty"`
	ErrorCode string    `json:"error_code,omitempty"`
}
type FilingResult struct {
	Key        string       `json:"key"`
	PlanDigest string       `json:"plan_digest"`
	State      string       `json:"state"`
	Steps      []StepResult `json:"steps"`
	ErrorCode  string       `json:"error_code,omitempty"`
}
type SourceRequest struct {
	Source Path `json:"source"`
}
type Source struct {
	Files []File `json:"files"`
}
type ImportPage struct {
	Entries []Path `json:"entries"`
	Next    string `json:"next"`
}
type EntriesRequest struct {
	Path Path `json:"path"`
}
type Entry struct {
	Name     string   `json:"name"`
	Dir      bool     `json:"dir"`
	Size     int64    `json:"size"`
	Identity Identity `json:"identity"`
}
type Entries struct {
	Entries []Entry `json:"entries"`
}
type Evidence struct {
	Index       int       `json:"index"`
	Source      *Identity `json:"source,omitempty"`
	Destination *Identity `json:"destination,omitempty"`
	ErrorCode   string    `json:"error_code,omitempty"`
}
type Inspection struct {
	Result   FilingResult `json:"result"`
	Evidence []Evidence   `json:"evidence"`
}

func CanonicalDigest(p FilingPlan) string {
	h := sha256.New()
	field := func(s string) { digestField(h, s) }
	number := func(n uint64) { var b [8]byte; binary.BigEndian.PutUint64(b[:], n); _, _ = h.Write(b[:]) }
	path := func(p Path) { field(p.Root); field(p.Rel) }
	id := func(i *Identity) {
		if i == nil {
			number(0)
			return
		}
		number(1)
		number(i.Device)
		number(i.Inode)
		number(uint64(i.Size))
		number(uint64(i.MTimeNS))
	}
	field("atrium-media-plan-v1")
	number(uint64(p.Version))
	field(p.AgentID)
	field(p.RootRevision)
	field(p.SourceKind)
	field(p.TorrentHash)
	number(uint64(len(p.Steps)))
	for _, s := range p.Steps {
		number(uint64(s.Index))
		field(s.Kind)
		if s.Source == nil {
			number(0)
		} else {
			number(1)
			path(s.Source.Path)
			id(&s.Source.Identity)
		}
		path(s.Destination)
		id(s.Expect)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func digestField(h hash.Hash, s string) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(len(s)))
	_, _ = h.Write(b[:])
	_, _ = h.Write([]byte(s))
}
func UndoKey(key string) string {
	sum := sha256.Sum256([]byte("atrium-media-v1-undo\n" + key))
	return hex.EncodeToString(sum[:])
}
