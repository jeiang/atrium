package tool

import (
	"context"
	"fmt"
	"unicode/utf8"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Bound struct {
	Key        string
	DefaultKey string
	Default    int64
	Min        int64
	Max        int64
	Literal    bool
}

func NewBound(key, defaultKey string) Bound {
	min, max, ok := settings.Default.Range(key)
	if !ok {
		panic("tool: setting " + key + " has no range")
	}
	return Bound{Key: key, DefaultKey: defaultKey, Min: min, Max: max}
}

func NewCeiling(def, min, max int64) Bound {
	return Bound{Default: def, Min: min, Max: max, Literal: true}
}

func (b Bound) Schema() string {
	return fmt.Sprintf(`{"type":"integer","minimum":%d,"maximum":%d}`, b.Min, b.Max)
}

func (b Bound) TextSchema() string {
	return fmt.Sprintf(`{"type":"string","maxLength":%d}`, b.Max)
}

func (b Bound) Clamp(ctx context.Context, q db.Querier, args Args, name string) (int64, error) {
	if b.Literal {
		return BoundedInt(args, name, b.Default, b.Max, b.Min)
	}
	r := assistant.Read(ctx, q)
	key := b.DefaultKey
	if key == "" {
		key = b.Key
	}
	def, liveMax := r.Int(key), r.Int(b.Key)
	if err := r.Err(); err != nil {
		return 0, err
	}
	return BoundedInt(args, name, int64(def), int64(liveMax), b.Min)
}

func (b Bound) ClampText(ctx context.Context, q db.Querier, text, name string) error {
	max := b.Max
	if !b.Literal {
		r := assistant.Read(ctx, q)
		key := b.DefaultKey
		if key == "" {
			key = b.Key
		}
		max = int64(r.Int(key))
		if err := r.Err(); err != nil {
			return err
		}
	}
	if utf8.RuneCountInString(text) > int(max) {
		return Error(fmt.Sprintf("%s is limited to %d characters", name, max))
	}
	return nil
}
