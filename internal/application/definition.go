package application

import (
	"reflect"
	"strings"
)

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
	AuditSet  bool
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
		AuditSet:  true,
		Input:     reflect.TypeFor[I](),
		Output:    reflect.TypeFor[O](),
	}
}

type BindingKind string

const (
	HTML         BindingKind = "html"
	API          BindingKind = "api"
	CLI          BindingKind = "cli"
	Tool         BindingKind = "tool"
	Job          BindingKind = "job"
	Service      BindingKind = "service"
	ProtocolOnly BindingKind = "protocol-only"
)

type Binding struct {
	Kind      BindingKind
	Target    string
	Uses      []Name
	Composite bool
	API       *APIShape
	Delegates []Name
	Reason    string
}

func Route(target string) (method, pattern string, ok bool) {
	method, pattern, ok = strings.Cut(target, " ")
	ok = ok && method != "" && method == strings.ToUpper(method) && strings.HasPrefix(pattern, "/") && !strings.ContainsAny(pattern, " \t")
	return method, pattern, ok
}
