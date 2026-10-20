package settings

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/py"
)

type Scope string

const (
	Instance Scope = "instance"
	Thread   Scope = "thread"
)

type Class string

const (
	Bootstrap Class = "bootstrap"
	Secret    Class = "secret"
	UI        Class = "ui"
	Advanced  Class = "advanced"
	Fixed     Class = "fixed"
	Cap       Class = "cap"
)

func (c Class) writable() bool { return c == UI || c == Advanced || c == Cap }

type Type string

const (
	String Type = "string"
	Int    Type = "int"
	Float  Type = "float"
	Bool   Type = "bool"
	Enum   Type = "enum"
)

type Def struct {
	Key                 string
	Module              string
	Type                Type
	Default             string
	Choices             []string
	Check               func(value string) error
	Class               Class
	Secret              bool
	RequiresEncryptedDB bool
	Generated           bool
	Restart             bool
	Scopes              []Scope
	ThreadColumn        string
}

func (d Def) has(s Scope) bool { return slices.Contains(d.Scopes, s) }

type PinSource interface {
	Pin(key string) (value, source string, ok bool)
}

type Registry struct {
	mu   sync.RWMutex
	defs map[string]Def
	pins PinSource
}

func New() *Registry { return &Registry{defs: map[string]Def{}} }

var Default = New()

func MustRegister(defs ...Def) {
	if err := Default.Register(defs...); err != nil {
		panic(err)
	}
}

func (r *Registry) Register(defs ...Def) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		if _, dup := r.defs[d.Key]; dup {
			return DuplicateKey(d.Key)
		}
		if err := d.valid(); err != nil {
			return err
		}
		r.defs[d.Key] = d
	}
	return nil
}

func (r *Registry) SetPins(p PinSource) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pins = p
	if p == nil {
		return
	}
	for _, d := range r.defs {
		if d.Secret && d.pinnable() {
			if value, _, ok := p.Pin(d.Key); ok {
				logx.AddSecret(value)
			}
		}
	}
}

func (r *Registry) Defs() []Def {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Def, 0, len(r.defs))
	for _, d := range r.defs {
		out = append(out, d.clone())
	}
	slices.SortFunc(out, func(a, b Def) int { return strings.Compare(a.Key, b.Key) })
	return out
}

func (r *Registry) Lookup(key string) (Def, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[key]
	return d.clone(), ok
}

func (d Def) clone() Def {
	d.Choices = slices.Clone(d.Choices)
	d.Scopes = slices.Clone(d.Scopes)
	return d
}

func (d Def) pinnable() bool { return d.Class != Fixed }

func (r *Registry) lookup(key string) (Def, PinSource, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.defs[key]
	if !ok {
		return Def{}, nil, UnknownKey(key)
	}
	return d, r.pins, nil
}

func (d Def) valid() error {
	bad := func(format string, a ...any) error {
		return InvalidDef(fmt.Sprintf("settings: %q: %s", d.Key, fmt.Sprintf(format, a...)))
	}
	switch {
	case !snakeCase(d.Key) || !snakeCase(d.Module):
		return InvalidDef(fmt.Sprintf("settings: key %q and module %q must be lowercase snake_case", d.Key, d.Module))
	case !slices.Contains([]Type{String, Int, Float, Bool, Enum}, d.Type):
		return bad("unknown type %q", d.Type)
	case !slices.Contains([]Class{Bootstrap, Secret, UI, Advanced, Fixed, Cap}, d.Class):
		return bad("unknown class %q", d.Class)
	case (d.Class == Secret) != d.Secret:
		return bad("the secret flag and the secret class go together")
	case (d.RequiresEncryptedDB || d.Generated) && d.Class != Secret:
		return bad("encrypted-database and generated flags belong to secret keys")
	case d.Class.writable() == (len(d.Scopes) == 0):
		return bad("class %s %s scopes", d.Class, map[bool]string{true: "needs", false: "takes no"}[d.Class.writable()])
	case (d.Type == Enum) != (len(d.Choices) > 0):
		return bad("choices belong to enum keys and enum keys need them")
	case d.Class == Cap && d.Type != Int && d.Type != Float:
		return bad("a cap key is a number")
	case d.has(Thread) != (d.ThreadColumn != ""):
		return bad("a thread column goes with the thread scope")
	case d.ThreadColumn != "" && !identifier(d.ThreadColumn):
		return bad("thread column %q is not an identifier", d.ThreadColumn)
	}
	for _, s := range d.Scopes {
		if s != Instance && s != Thread {
			return bad("unknown scope %q", s)
		}
	}
	if d.Default != "" {
		if err := d.validate(d.Default); err != nil {
			return bad("default: %v", err)
		}
	}
	return nil
}

func snakeCase(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' || s[len(s)-1] == '_' || strings.Contains(s, "__") {
		return false
	}
	for _, c := range s {
		if c != '_' && (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return false
		}
	}
	return true
}

func identifier(s string) bool {
	for _, c := range s {
		if c != '_' && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

func (d Def) validate(value string) error {
	switch d.Type {
	case Int:
		if _, ok := py.ParseInt(value); !ok {
			return Invalid("must be a whole number")
		}
	case Float:
		if _, err := number(value); err != nil {
			return err
		}
	case Bool:
		if value != "true" && value != "false" {
			return Invalid("must be true or false")
		}
	case Enum:
		if !slices.Contains(d.Choices, value) {
			return Invalid("must be one of " + strings.Join(d.Choices, ", "))
		}
	}
	if d.Check != nil {
		return d.Check(value)
	}
	return nil
}

func (r *Registry) Validate(key, value string) error {
	d, _, err := r.lookup(key)
	if err != nil {
		return err
	}
	if err := d.validate(value); err != nil {
		return Invalid(key + " " + err.Error())
	}
	return nil
}

type Invalid string

func (e Invalid) Error() string { return string(e) }

type InvalidDef string

func (e InvalidDef) Error() string { return string(e) }

type DuplicateKey string

func (e DuplicateKey) Error() string { return "settings: duplicate key " + strconv.Quote(string(e)) }

type UnknownKey string

func (e UnknownKey) Error() string { return "settings: unknown key " + strconv.Quote(string(e)) }

type UnknownThread int64

func (e UnknownThread) Error() string {
	return "settings: no thread " + strconv.FormatInt(int64(e), 10)
}

type ScopeError struct {
	Key   string
	Scope Scope
}

func (e *ScopeError) Error() string {
	return fmt.Sprintf("settings: %s cannot be set at the %s level", e.Key, e.Scope)
}

type PinnedError struct {
	Key    string
	Source string
}

func (e *PinnedError) Error() string {
	return fmt.Sprintf("settings: %s is pinned by %s", e.Key, e.Source)
}
