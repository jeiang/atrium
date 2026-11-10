package settings

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/jeiang/atrium/internal/logx"
)

type Kind string

const (
	DeployKind Kind = "deploy"
	ValueKind  Kind = "value"
	SecretKind Kind = "secret"
)

type Surface string

const (
	Advanced Surface = "advanced"
	UI       Surface = "ui"
)

type Type string

const (
	String Type = "string"
	Int    Type = "int"
	Float  Type = "float"
	Bool   Type = "bool"
	Enum   Type = "enum"
)

type Def struct {
	Key       string
	Label     string
	Module    string
	Type      Type
	Default   string
	Min       int64
	Max       int64
	Choices   []string
	Check     func(value string) error
	Peers     func(value string, peer func(key string) (string, error)) error
	EnvAlias  string
	Kind      Kind
	Surface   Surface
	Encrypted bool
	Generated bool
	PinOnly   bool
	Readable  bool
}

type SecretOpts struct {
	Encrypted bool
	Generated bool
	PinOnly   bool
	Readable  bool
}

func Deploy(d Def) Def {
	d.Kind = DeployKind
	return d
}

func Value(surface Surface, d Def) Def {
	d.Kind = ValueKind
	d.Surface = surface
	return d
}

func Shown(label string, d Def) Def {
	d.Label = label
	d.Surface = UI
	return d
}

func Secret(opts SecretOpts, d Def) Def {
	d.Kind = SecretKind
	d.Encrypted, d.Generated, d.PinOnly, d.Readable = opts.Encrypted, opts.Generated, opts.PinOnly, opts.Readable
	return d
}

func (d Def) Editable() bool { return d.Kind == ValueKind }

func (d Def) IsSecret() bool { return d.Kind == SecretKind }

func (d Def) WriteOnly() bool { return d.Kind == SecretKind && !d.Readable }

func (d Def) NeedsEncryptedDB() bool { return d.Kind == SecretKind && d.Encrypted }

func (d Def) IsGenerated() bool { return d.Kind == SecretKind && d.Generated }

func (d Def) PinnedOnly() bool { return d.Kind == SecretKind && d.PinOnly }

func (d Def) Class() string {
	switch d.Kind {
	case DeployKind:
		return "bootstrap"
	case SecretKind:
		return "secret"
	default:
		return string(d.Surface)
	}
}

type PinSource interface {
	Pin(key string) (value, source string, ok bool)
}

type Registry struct {
	mu   sync.RWMutex
	defs map[string]Def
	pins PinSource
}

var definitions []Def

func New() *Registry {
	r := &Registry{defs: make(map[string]Def, len(definitions))}
	for _, d := range definitions {
		r.defs[d.Key] = d
	}
	return r
}

func MustRegister(defs ...Def) {
	for _, d := range defs {
		if slices.ContainsFunc(definitions, func(e Def) bool { return e.Key == d.Key }) {
			panic(DuplicateKey(d.Key))
		}
		if err := d.valid(); err != nil {
			panic(err)
		}
		definitions = append(definitions, d)
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
		if d.IsSecret() {
			if value, _, ok := p.Pin(d.Key); ok {
				logx.AddSecret(value)
			}
		}
	}
}

func (r *Registry) PinSource() PinSource {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.pins
}

func (r *Registry) Pin(key string) (value, source string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.pins == nil {
		return "", "", false
	}
	return r.pins.Pin(key)
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

func LookupOf(key string) (Def, bool) {
	for _, d := range definitions {
		if d.Key == key {
			return d.clone(), true
		}
	}
	return Def{}, false
}

func RangeOf(key string) (int64, int64, bool) {
	for _, d := range definitions {
		if d.Key == key {
			if d.Max <= 0 {
				return 0, 0, false
			}
			return d.Min, d.Max, true
		}
	}
	return 0, 0, false
}

func (d Def) clone() Def {
	d.Choices = slices.Clone(d.Choices)
	return d
}

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
	case !slices.Contains([]Kind{DeployKind, ValueKind, SecretKind}, d.Kind):
		return bad("unknown kind %q", d.Kind)
	case d.Kind == ValueKind && !slices.Contains([]Surface{UI, Advanced}, d.Surface):
		return bad("a value needs the ui or advanced surface")
	case d.Kind != ValueKind && d.Surface != "":
		return bad("only a value has a surface")
	case d.Kind != SecretKind && (d.Encrypted || d.Generated || d.PinOnly || d.Readable):
		return bad("encrypted, generated, pin-only and readable belong to secret keys")
	case (d.Type == Enum) != (len(d.Choices) > 0):
		return bad("choices belong to enum keys and enum keys need them")
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

func (d Def) validate(value string) error {
	switch d.Type {
	case Int:
		if _, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err != nil {
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

type FieldInvalid struct {
	Key string
	Err Invalid
}

func (e FieldInvalid) Error() string { return e.Err.Error() }

func (e FieldInvalid) Unwrap() error { return e.Err }

type InvalidDef string

func (e InvalidDef) Error() string { return string(e) }

type DuplicateKey string

func (e DuplicateKey) Error() string { return "settings: duplicate key " + strconv.Quote(string(e)) }

type UnknownKey string

func (e UnknownKey) Error() string { return "settings: unknown key " + strconv.Quote(string(e)) }

type NotEditable struct{ Key string }

func (e *NotEditable) Error() string { return "settings: " + e.Key + " is not editable" }

type PinnedError struct {
	Key    string
	Source string
}

func (e *PinnedError) Error() string {
	return fmt.Sprintf("settings: %s is pinned by %s", e.Key, e.Source)
}
