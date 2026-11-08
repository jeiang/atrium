package tool

import (
	"context"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Bound struct {
	Key        string
	DefaultKey string
	Min        int64
	Max        int64
}

func NewBound(key, defaultKey string) Bound {
	min, max, ok := settings.Default.Range(key)
	if !ok {
		panic("tool: setting " + key + " has no range")
	}
	return Bound{Key: key, DefaultKey: defaultKey, Min: min, Max: max}
}

func (b Bound) Schema() string {
	return fmt.Sprintf(`{"type":"integer","minimum":%d,"maximum":%d}`, b.Min, b.Max)
}

func (b Bound) Clamp(ctx context.Context, q db.Querier, args Args, name string) (int64, error) {
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
