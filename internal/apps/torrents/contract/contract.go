package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

const (
	Kind connections.Kind = "service/qbittorrent"

	OpAddMagnet = "media_add_magnet"
	OpAddFile   = "media_add_file"
	UndoTorrent = "media_torrent_delete"

	TagAtrium = "atrium"
	TagLegacy = "media"

	PriorityNormal = 1
	PrioritySkip   = 0

	StoppedUP = "stoppedUP"
	StoppedDL = "stoppedDL"
)

var (
	StateLabels = map[string]string{
		"downloading": "downloading", "metaDL": "fetching metadata", "forcedMetaDL": "fetching metadata",
		"stalledDL": "stalled", "checkingDL": "checking", "forcedDL": "downloading", "queuedDL": "queued",
		"allocating": "allocating", "moving": "moving", "checkingResumeData": "checking", "uploading": "seeding",
		"forcedUP": "seeding", "stalledUP": "seeding", "queuedUP": "queued", "checkingUP": "checking",
		StoppedUP: "done", StoppedDL: "stopped", "pausedUP": "done", "pausedDL": "stopped", "error": "error",
		"missingFiles": "missing files", "unknown": "unknown",
	}

	activeStates = map[string]bool{
		"downloading": true, "metaDL": true, "forcedMetaDL": true, "stalledDL": true,
		"checkingDL": true, "forcedDL": true, "queuedDL": true, "allocating": true,
		"moving": true, "checkingResumeData": true, "uploading": true, "forcedUP": true,
		"checkingUP": true,
	}

	stoppedStates = map[string]bool{StoppedUP: true, StoppedDL: true, "pausedUP": true, "pausedDL": true}
)

type QbitError string

func (e QbitError) Error() string { return string(e) }

type QbitStatusError struct {
	Path   string
	Status int
}

func (e *QbitStatusError) Error() string {
	return fmt.Sprintf("qBittorrent: %s: %d", e.Path, e.Status)
}

type QbitRejected string

func (e QbitRejected) Error() string { return string(e) }

func IsQbit(err error) bool {
	var failure QbitError
	var rejected QbitRejected
	var status *QbitStatusError
	return errors.As(err, &failure) || errors.As(err, &rejected) || errors.As(err, &status)
}

func QbitDefinite(err error) bool {
	var rejected QbitRejected
	if errors.As(err, &rejected) {
		return true
	}
	var status *QbitStatusError
	if errors.As(err, &status) {
		return status.Status >= 400 && status.Status < 500
	}
	return false
}

type Invalid string

func (e Invalid) Error() string { return string(e) }

type Reason string

const (
	Missing      Reason = "the torrent is not in qBittorrent"
	Untagged     Reason = "the torrent has no atrium tag"
	Unlabelled   Reason = "the torrent does not carry the label"
	NotConsumed  Reason = "the torrent is not marked consumed"
	NotStopped   Reason = "the torrent is not stopped"
	Protected    Reason = "provenance tags are never removed"
	UnknownLabel Reason = "the label has no registered consumer"
)

type Refused struct {
	Hash   string
	Reason Reason
}

func (e *Refused) Error() string { return "torrents refused the write: " + string(e.Reason) }

type Torrent struct {
	Hash        string  `json:"hash"`
	Name        string  `json:"name"`
	State       string  `json:"state"`
	Tags        string  `json:"tags"`
	ContentPath string  `json:"content_path"`
	Progress    float64 `json:"progress"`
	Size        *int64  `json:"size"`
	DLSpeed     *int64  `json:"dlspeed"`
	UpSpeed     *int64  `json:"upspeed"`
	ETA         *int64  `json:"eta"`
}

type TorrentFile struct {
	Index    int     `json:"index"`
	Name     string  `json:"name"`
	Priority int     `json:"priority"`
	Size     *int64  `json:"size"`
	Progress float64 `json:"progress"`
}

func (t Torrent) Active() bool { return activeStates[t.State] }

func (t Torrent) Stopped() bool { return stoppedStates[t.State] }

func (t Torrent) Provenance() bool { return t.HasTag(TagAtrium) || t.HasTag(TagLegacy) }

func (t Torrent) HasTag(tag string) bool {
	for part := range strings.SplitSeq(t.Tags, ",") {
		if strings.TrimSpace(part) == tag {
			return true
		}
	}
	return false
}

type Actor string

const (
	Operator Actor = "operator"
	Agent    Actor = "agent"
	Job      Actor = "job"
)

type Scope struct {
	Actor      Actor
	ProposalID *int64
	ThreadID   *int64
	RunID      *int64
}

type Addition struct {
	Hash  string
	Scope Scope
}

type Consumer struct {
	Label    string
	Consumed string
	Title    string
	Skipped  string
	Added    func(ctx context.Context, d Deps, in Intake, a Addition) error
}

type AddRequest struct {
	Magnet       string
	Data         []byte
	Name         string
	Label        string
	OperationKey string
	Scope        Scope
	Done         ledger.Mutation
}

type Filter struct {
	Completed bool
	Label     string
}

type Action string

const (
	Pause  Action = "pause"
	Resume Action = "resume"
	Remove Action = "remove"
)

type Control struct {
	Scope       Scope
	Hash        string
	Label       string
	Action      Action
	DeleteFiles bool
}

type Intake interface {
	Ready(ctx context.Context) error
	List(ctx context.Context, f Filter) ([]Torrent, error)
	Get(ctx context.Context, hash string) (*Torrent, error)
	Files(ctx context.Context, hash string) ([]TorrentFile, error)
	Add(ctx context.Context, req AddRequest) (string, error)
	SetWanted(ctx context.Context, scope Scope, hash, label string, ids []int, wanted bool) error
	Mark(ctx context.Context, scope Scope, hash, label, mark string, on bool) error
	Control(ctx context.Context, c Control) error
}

type Deps struct {
	DB     *db.DB
	Env    config.Env
	Ledger *ledger.Store
}

type Provider interface {
	Intake(d Deps) Intake
}
