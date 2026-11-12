package application

import "reflect"

type Name string

type Kind string

const (
	Command Kind = "command"
	Query   Kind = "query"
)

type Authority string

const (
	Operator         Authority = "operator"
	Agent            Authority = "agent"
	OperatorSend     Authority = "operator_send"
	Approved         Authority = "approved"
	System           Authority = "system"
	PublicCapability Authority = "public_capability"
	Bootstrap        Authority = "bootstrap"
)

type Effect string

const (
	NoEffect      Effect = "none"
	LocalUndoable Effect = "local_undoable"
	External      Effect = "external"
	Conditional   Effect = "conditional"
)

type Definition struct {
	Name      Name
	Kind      Kind
	Authority []Authority
	Effect    Effect
	Audit     bool
	Input     reflect.Type
	Output    reflect.Type
}

func Describe[I, O any](name Name, kind Kind, authority []Authority, effect Effect, audit bool) Definition {
	return Definition{
		Name:      name,
		Kind:      kind,
		Authority: authority,
		Effect:    effect,
		Audit:     audit,
		Input:     reflect.TypeFor[I](),
		Output:    reflect.TypeFor[O](),
	}
}

type BindingKind string

const (
	HTML    BindingKind = "html"
	API     BindingKind = "api"
	CLI     BindingKind = "cli"
	Tool    BindingKind = "tool"
	Job     BindingKind = "job"
	Service BindingKind = "service"
)

type Binding struct {
	Kind   BindingKind
	Target string
	Uses   []Name
}
