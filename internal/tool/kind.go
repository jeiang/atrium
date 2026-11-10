package tool

import "slices"

type Kind string

const (
	Chat    Kind = "chat"
	Cron    Kind = "cron"
	Nightly Kind = "nightly"
	Sub     Kind = "sub"
	Worker  Kind = "worker"
)

func ParseKind(value string) (Kind, bool) {
	switch Kind(value) {
	case Chat, Cron, Nightly, Sub, Worker:
		return Kind(value), true
	}
	return "", false
}

type Kinds []Kind

var (
	AllKinds     = Kinds{Chat, Cron, Nightly, Sub}
	DefaultKinds = Kinds{Chat, Cron, Nightly}
)

func (ks Kinds) Has(k Kind) bool { return slices.Contains(ks, k) }

func (k Kind) Valid() bool { return AllKinds.Has(k) || k == Worker }

type Effect string

const (
	Read      Effect = "read"
	WriteUndo Effect = "write_undo"
	Propose   Effect = "propose"
	Notify    Effect = "notify"
)

var Effects = []Effect{Read, WriteUndo, Propose, Notify}

func (e Effect) Valid() bool { return slices.Contains(Effects, e) }
