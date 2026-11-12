package settingspage

import (
	"context"
	"strconv"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

type Field = settings.Field

type Section struct {
	Name, Title, Module string
	Template            string
	Fields              []settings.Field
	Validate            func(ctx context.Context, q db.Querier, values map[string]string) error
	Data                func(ctx context.Context, q db.Querier) (any, error)
}

type SectionView struct {
	Module   string               `json:"module"`
	Name     string               `json:"name"`
	Title    string               `json:"title"`
	Template string               `json:"template"`
	Fields   []settings.FieldView `json:"fields"`
	Data     any                  `json:"data,omitempty"`
}

type Sections []Section

func (s Sections) Named(name string) (Section, bool) {
	for _, section := range s {
		if section.Name == name {
			return section, true
		}
	}
	return Section{}, false
}

func (s Sections) Load(ctx context.Context, q db.Querier) ([]SectionView, error) {
	var out []SectionView
	for _, section := range s {
		fields, err := settings.LoadFields(ctx, q, section.Fields)
		if err != nil {
			return nil, err
		}
		var data any
		if section.Data != nil {
			if data, err = section.Data(ctx, q); err != nil {
				return nil, err
			}
		}
		out = append(out, SectionView{Module: section.Module, Name: section.Name, Title: section.Title, Template: section.Template, Fields: fields, Data: data})
	}
	return out, nil
}

func (s Sections) Save(ctx context.Context, d *db.DB, name string, values map[string]string) error {
	section, ok := s.Named(name)
	if !ok {
		return UnknownSection(name)
	}
	if section.Validate != nil {
		if err := section.Validate(ctx, d.Read, values); err != nil {
			return err
		}
	}
	return settings.SaveFields(ctx, d, section.Fields, values)
}

func (s Sections) Modules() map[string]bool {
	out := make(map[string]bool, len(s))
	for _, section := range s {
		out[section.Module] = true
	}
	return out
}

type UnknownSection string

func (e UnknownSection) Error() string { return "unknown section " + strconv.Quote(string(e)) }

type DuplicateSection string

func (e DuplicateSection) Error() string { return "duplicate section " + strconv.Quote(string(e)) }
