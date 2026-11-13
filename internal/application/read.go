package application

type Revision string

type Read[T any] struct {
	Revision Revision `json:"revision"`
	Data     T        `json:"data"`
}

type Change[T any] struct {
	Data      T       `json:"data"`
	EffectIDs []int64 `json:"effect_ids,omitempty"`
	AuditID   int64   `json:"audit_id,omitempty"`
}

type Page struct {
	Limit  int
	Cursor string
}

type Items[T any] struct {
	Items      []T
	NextCursor string
}
