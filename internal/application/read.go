package application

import "strings"

type Revision string

func (r Revision) Matches(ifMatch string) bool {
	if strings.TrimSpace(ifMatch) == "" {
		return true
	}
	for tag := range strings.SplitSeq(ifMatch, ",") {
		tag = strings.TrimSpace(tag)
		if tag == "*" || strings.Trim(strings.TrimPrefix(tag, "W/"), `"`) == string(r) {
			return true
		}
	}
	return false
}

type Read[T any] struct {
	Revision Revision `json:"revision"`
	Data     T        `json:"data"`
}

type Change[T any] struct {
	Data      T       `json:"data"`
	EffectIDs []int64 `json:"effect_ids,omitempty"`
	Undoable  *bool   `json:"undoable,omitempty"`
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
