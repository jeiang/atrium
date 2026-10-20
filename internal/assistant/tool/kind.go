package tool

import "slices"

type Kind string

const (
	Chat    Kind = "chat"
	Cron    Kind = "cron"
	Nightly Kind = "nightly"
	Sub     Kind = "sub"
)

type Kinds []Kind

var (
	AllKinds     = Kinds{Chat, Cron, Nightly, Sub}
	DefaultKinds = Kinds{Chat, Cron, Nightly}
)

func (ks Kinds) Has(k Kind) bool { return slices.Contains(ks, k) }

func (k Kind) Valid() bool { return AllKinds.Has(k) }

func (k Kind) Unattended() bool { return k == Cron || k == Nightly }

type Effect string

const (
	Read    Effect = "read"
	Write   Effect = "write"
	Propose Effect = "propose"
	Notify  Effect = "notify"
)

var Effects = []Effect{Read, Write, Propose, Notify}

func (e Effect) Valid() bool { return slices.Contains(Effects, e) }
