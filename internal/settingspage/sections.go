package settingspage

import (
	"context"
	"slices"
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
	Module   string              `json:"module"`
	Name     string              `json:"name"`
	Title    string              `json:"title"`
	Template string              `json:"template"`
	Fields   []settings.FieldView `json:"fields"`
	Data     any                 `json:"data,omitempty"`
}

var sections []Section

func RegisterSection(s Section) {
	if slices.ContainsFunc(sections, func(r Section) bool { return r.Name == s.Name }) {
		return
	}
	sections = append(sections, s)
	Want(s.Module)
}

func AllSections() []Section { return slices.Clone(sections) }

func SectionNamed(name string) (Section, bool) {
	for _, s := range AllSections() {
		if s.Name == name {
			return s, true
		}
	}
	return Section{}, false
}

func LoadSections(ctx context.Context, q db.Querier) ([]SectionView, error) {
	var out []SectionView
	for _, s := range AllSections() {
		fields, err := settings.LoadFields(ctx, q, s.Fields)
		if err != nil {
			return nil, err
		}
		var data any
		if s.Data != nil {
			if data, err = s.Data(ctx, q); err != nil {
				return nil, err
			}
		}
		out = append(out, SectionView{Module: s.Module, Name: s.Name, Title: s.Title, Template: s.Template, Fields: fields, Data: data})
	}
	return out, nil
}

func SaveSection(ctx context.Context, d *db.DB, name string, values map[string]string) error {
	s, ok := SectionNamed(name)
	if !ok {
		return UnknownSection(name)
	}
	if s.Validate != nil {
		if err := s.Validate(ctx, d.Read, values); err != nil {
			return err
		}
	}
	return settings.SaveFields(ctx, d, s.Fields, values)
}

type UnknownSection string

func (e UnknownSection) Error() string { return "unknown section " + strconv.Quote(string(e)) }
