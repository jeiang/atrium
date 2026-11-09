package settings

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type Field struct {
	Key     string
	Label   string
	Number  bool
	Whole   bool
	Choices []string
}

type FieldView struct {
	Key     string   `json:"key"`
	Label   string   `json:"label"`
	Value   string   `json:"value"`
	Number  bool     `json:"number"`
	Whole   bool     `json:"whole"`
	Choices []string `json:"choices"`
	Pinned  bool     `json:"pinned"`
}

func LoadFields(ctx context.Context, q db.Querier, fields []Field) ([]FieldView, error) {
	reg := From(ctx)
	out := make([]FieldView, 0, len(fields))
	for _, f := range fields {
		found, err := reg.Resolve(ctx, q, f.Key)
		if err != nil {
			return nil, err
		}
		def, _ := reg.Lookup(f.Key)
		out = append(out, FieldView{
			Key: f.Key, Label: f.Label, Value: strings.TrimSpace(found.Value),
			Number: f.Number, Whole: f.Whole, Choices: f.Choices,
			Pinned: found.Pinned() || !def.Editable(),
		})
	}
	return out, nil
}

func SaveFields(ctx context.Context, d *db.DB, fields []Field, values map[string]string) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		reg := From(ctx)
		for _, f := range fields {
			value, present := values[f.Key]
			if !present {
				continue
			}
			if value = strings.TrimSpace(value); value == "" {
				if err := reg.Unset(ctx, tx, f.Key); err != nil {
					return err
				}
				continue
			}
			if err := reg.Set(ctx, tx, f.Key, value); err != nil {
				return err
			}
		}
		return nil
	})
}
