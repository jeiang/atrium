package application

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
)

type BodyKind string

const (
	BodyJSON        BodyKind = "json"
	BodyNone        BodyKind = "none"
	BodyMultipart   BodyKind = "multipart"
	BodyBinary      BodyKind = "binary"
	BodyEventStream BodyKind = "event-stream"
)

func (b BodyKind) Valid() bool {
	switch b {
	case BodyJSON, BodyNone, BodyMultipart, BodyBinary, BodyEventStream:
		return true
	}
	return false
}

type APIShape struct {
	Area         Area
	Request      reflect.Type
	Response     reflect.Type
	Success      int
	Body         BodyKind
	BodyLimit    int64
	AllowJSON    bool
	IgnoreBody   bool
	BeforeBody   func(context.Context) error
	ID           string
	Uncached     bool
	ReadOnlyPost bool
	Sensitive    bool
	Unowned      bool
	Errors       []Code
}

var Codes = []Code{Invalid, BadCursor, NotFound, Unauthenticated, Forbidden, Conflict, Refused, NotConfigured, RateLimited, Budget, Unavailable, Unknown, Partial, Canceled, Internal}

func RequiresAPI(authority []Authority) bool {
	return slices.ContainsFunc(authority, func(a Authority) bool { return a == Operator || a == OperatorSend || a == Approved })
}

const UnionDiscriminator = "kind"

type Union struct {
	Interface reflect.Type
	Variants  map[string]reflect.Type
}

func (u Union) Kinds() []string { return slices.Sorted(maps.Keys(u.Variants)) }

var (
	unions   = map[reflect.Type]Union{}
	variants = map[reflect.Type]string{}
)

func RegisterUnion[T any](kinds map[string]reflect.Type) {
	iface := reflect.TypeFor[T]()
	if iface.Kind() != reflect.Interface || unions[iface].Interface != nil {
		panic(fmt.Sprintf("application: union %s is not a new interface", iface))
	}
	for kind, t := range kinds {
		f, ok := t.FieldByName("Kind")
		if t.Kind() != reflect.Struct || !t.Implements(iface) || !ok || f.Tag.Get("json") != UnionDiscriminator {
			panic(fmt.Sprintf("application: union %s variant %s must be a struct implementing it with a Kind field tagged json:%q", iface, t, UnionDiscriminator))
		}
		variants[t] = kind
	}
	unions[iface] = Union{Interface: iface, Variants: maps.Clone(kinds)}
}

func UnionOf(t reflect.Type) (Union, bool) {
	u, ok := unions[t]
	return u, ok
}

func VariantKind(t reflect.Type) (string, bool) {
	kind, ok := variants[t]
	return kind, ok
}
