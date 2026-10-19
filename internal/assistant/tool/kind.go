// Package tool is the contract every assistant tool is written against: the Tool value the registry holds, the
// Context a handler runs with, the error classes the loop shows the model and the argument helpers of
// agent/tools/_args.py. Phase A keeps exactly what Python's Tool dataclass carries.
package tool

import "slices"

// Kind is the run kind a tool is offered in. The Python `code` kind (run_code stubs) is not ported.
type Kind string

const (
	Chat    Kind = "chat"
	Cron    Kind = "cron"
	Nightly Kind = "nightly"
	// Sub is a research subagent; the run's allowlist narrows its tools further.
	Sub Kind = "sub"
)

// Kinds is a set of run kinds.
type Kinds []Kind

var (
	// AllKinds is ALL_KINDS without the unported code kind.
	AllKinds = Kinds{Chat, Cron, Nightly, Sub}
	// DefaultKinds is the Python Tool default contexts: {chat, cron, nightly}.
	DefaultKinds = Kinds{Chat, Cron, Nightly}
)

// Has reports whether k is in the set.
func (ks Kinds) Has(k Kind) bool { return slices.Contains(ks, k) }

// Valid reports whether k is one of AllKinds.
func (k Kind) Valid() bool { return AllKinds.Has(k) }

// Unattended is UNATTENDED: the kinds that run without an operator.
func (k Kind) Unattended() bool { return k == Cron || k == Nightly }

// Effect classifies what a tool changes; it decides the journal replay class of a call.
type Effect string

const (
	Read    Effect = "read"
	Write   Effect = "write"
	Propose Effect = "propose"
	Notify  Effect = "notify"
)

// Effects is EFFECTS.
var Effects = []Effect{Read, Write, Propose, Notify}

// Valid reports whether e is one of Effects.
func (e Effect) Valid() bool { return slices.Contains(Effects, e) }
