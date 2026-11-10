package settings

import (
	"context"
	"database/sql"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/db"
)

type Field struct {
	Key          string
	Label        string
	Number       bool
	Whole        bool
	Bool         bool
	Multi        bool
	Choices      []string
	ChoiceLabels map[string]string
}

type FieldOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Checked bool   `json:"checked"`
}

type FieldView struct {
	Key     string        `json:"key"`
	Label   string        `json:"label"`
	Value   string        `json:"value"`
	Number  bool          `json:"number"`
	Whole   bool          `json:"whole"`
	Bool    bool          `json:"bool"`
	Multi   bool          `json:"multi"`
	Choices []string      `json:"choices"`
	Options []FieldOption `json:"options"`
	Pinned  bool          `json:"pinned"`
}

type Group struct {
	Module string
	Name   string
	Title  string
	Page   string
	Keys   []string
}

type GroupView struct {
	Module string      `json:"module"`
	Name   string      `json:"name"`
	Title  string      `json:"title"`
	Fields []FieldView `json:"fields"`
	Saved  bool        `json:"saved"`
	Error  string      `json:"error"`
}

func (r *Registry) Fields(keys []string) ([]Field, error) {
	out := make([]Field, 0, len(keys))
	for _, key := range keys {
		def, ok := r.Lookup(key)
		if !ok {
			return nil, UnknownKey(key)
		}
		label := def.Label
		if label == "" {
			label = key
		}
		out = append(out, Field{
			Key: key, Label: label, Choices: def.Choices,
			Bool: def.Type == Bool, Number: def.Type == Int || def.Type == Float, Whole: def.Type == Int,
		})
	}
	return out, nil
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
		view := FieldView{
			Key: f.Key, Label: f.Label, Value: strings.TrimSpace(found.Value),
			Number: f.Number, Whole: f.Whole, Bool: f.Bool, Multi: f.Multi, Choices: f.Choices,
			Pinned: found.Pinned() || !def.Editable(),
		}
		if f.Multi {
			selected := strings.Split(view.Value, ",")
			for _, choice := range f.Choices {
				label := choice
				if f.ChoiceLabels[choice] != "" {
					label = f.ChoiceLabels[choice]
				}
				view.Options = append(view.Options, FieldOption{Value: choice, Label: label, Checked: slices.Contains(selected, choice)})
			}
		}
		out = append(out, view)
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
