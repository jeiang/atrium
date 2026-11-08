package notion

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/notionid"
)

type RemotePage struct {
	ID             string              `json:"id"`
	URL            string              `json:"url"`
	LastEditedTime string              `json:"last_edited_time"`
	InTrash        bool                `json:"in_trash"`
	Parent         Parent              `json:"parent"`
	Properties     map[string]Property `json:"properties"`
}

type Parent struct {
	Type         string `json:"type"`
	PageID       string `json:"page_id"`
	DatabaseID   string `json:"database_id"`
	DataSourceID string `json:"data_source_id"`
	Workspace    bool   `json:"workspace"`
}

func (p Parent) Ref() (kind, id string) {
	switch p.Type {
	case "workspace":
		return "workspace", ""
	case "page_id":
		return "page_id", notionid.Normalize(p.PageID)
	case "database_id":
		return "database_id", notionid.Normalize(p.DatabaseID)
	case "data_source_id":
		return "data_source_id", notionid.Normalize(p.DataSourceID)
	}
	return p.Type, ""
}

type Property struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	Title       []RichText `json:"title"`
	RichText    []RichText `json:"rich_text"`
	Select      *Option    `json:"select"`
	Status      *Option    `json:"status"`
	MultiSelect []Option   `json:"multi_select"`
	Date        *DateValue `json:"date"`
	Number      *float64   `json:"number"`
	Checkbox    *bool      `json:"checkbox"`
	URL         *string    `json:"url"`
	Email       *string    `json:"email"`
	PhoneNumber *string    `json:"phone_number"`
	Relation    []Relation `json:"relation"`
	People      []Person   `json:"people"`
	Formula     *Formula   `json:"formula"`
	Rollup      *Rollup    `json:"rollup"`
}

type RichText struct {
	PlainText string `json:"plain_text"`
	Href      string `json:"href"`
}

type Option struct {
	Name string `json:"name"`
}

type DateValue struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

type Relation struct {
	ID string `json:"id"`
}

type Person struct {
	Name string `json:"name"`
}

type Formula struct {
	Type    string     `json:"type"`
	String  *string    `json:"string"`
	Number  *float64   `json:"number"`
	Boolean *bool      `json:"boolean"`
	Date    *DateValue `json:"date"`
}

type Rollup struct {
	Type   string     `json:"type"`
	Number *float64   `json:"number"`
	Date   *DateValue `json:"date"`
	Array  []Property `json:"array"`
}

func (p Property) Text() string {
	switch p.Type {
	case "title":
		return richTextText(p.Title)
	case "rich_text":
		return richTextText(p.RichText)
	case "select":
		return optionName(p.Select)
	case "status":
		return optionName(p.Status)
	case "multi_select":
		names := make([]string, 0, len(p.MultiSelect))
		for _, o := range p.MultiSelect {
			names = append(names, o.Name)
		}
		return strings.Join(names, ", ")
	case "date":
		return dateValueText(p.Date)
	case "number":
		return numberText(p.Number)
	case "checkbox":
		return boolText(p.Checkbox)
	case "url":
		return derefString(p.URL)
	case "email":
		return derefString(p.Email)
	case "phone_number":
		return derefString(p.PhoneNumber)
	case "relation":
		if len(p.Relation) == 0 {
			return ""
		}
		return strconv.Itoa(len(p.Relation)) + " linked"
	case "people":
		names := make([]string, 0, len(p.People))
		for _, person := range p.People {
			names = append(names, person.Name)
		}
		return strings.Join(names, ", ")
	case "formula":
		return formulaText(p.Formula)
	case "rollup":
		return rollupText(p.Rollup)
	}
	return ""
}

func (p RemotePage) Title() string {
	for _, prop := range p.Properties {
		if prop.Type == "title" {
			return strings.TrimSpace(prop.Text())
		}
	}
	return ""
}

func richTextText(items []RichText) string {
	var b strings.Builder
	for _, item := range items {
		b.WriteString(item.PlainText)
	}
	return b.String()
}

func optionName(o *Option) string {
	if o == nil {
		return ""
	}
	return o.Name
}

func dateValueText(d *DateValue) string {
	if d == nil {
		return ""
	}
	switch {
	case d.Start != "" && d.End != "":
		return d.Start + " → " + d.End
	case d.Start != "":
		return d.Start
	}
	return d.End
}

func numberText(n *float64) string {
	if n == nil {
		return ""
	}
	return strconv.FormatFloat(*n, 'f', -1, 64)
}

func boolText(b *bool) string {
	if b == nil {
		return ""
	}
	return strconv.FormatBool(*b)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func formulaText(f *Formula) string {
	if f == nil {
		return ""
	}
	switch f.Type {
	case "string":
		return derefString(f.String)
	case "number":
		return numberText(f.Number)
	case "boolean":
		return boolText(f.Boolean)
	case "date":
		return dateValueText(f.Date)
	}
	return ""
}

func rollupText(r *Rollup) string {
	if r == nil {
		return ""
	}
	switch r.Type {
	case "number":
		return numberText(r.Number)
	case "date":
		return dateValueText(r.Date)
	case "array":
		parts := make([]string, 0, len(r.Array))
		for _, p := range r.Array {
			if text := p.Text(); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, ", ")
	}
	return ""
}

type SearchResult struct {
	Object         string              `json:"object"`
	ID             string              `json:"id"`
	URL            string              `json:"url"`
	LastEditedTime string              `json:"last_edited_time"`
	InTrash        bool                `json:"in_trash"`
	Parent         Parent              `json:"parent"`
	DatabaseParent Parent              `json:"database_parent"`
	Title          []RichText          `json:"title"`
	Properties     map[string]Property `json:"properties"`
}

func (r SearchResult) TitleText() string { return strings.TrimSpace(richTextText(r.Title)) }

func (r SearchResult) RemotePage() RemotePage {
	return RemotePage{ID: r.ID, URL: r.URL, LastEditedTime: r.LastEditedTime, InTrash: r.InTrash, Parent: r.Parent, Properties: r.Properties}
}

type Markdown struct {
	Markdown  string `json:"markdown"`
	Truncated bool   `json:"truncated"`
}

type Block struct {
	ID          string     `json:"id"`
	Type        string     `json:"type"`
	HasChildren bool       `json:"has_children"`
	Paragraph   *TextBlock `json:"paragraph"`
	Heading1    *TextBlock `json:"heading_1"`
	Heading2    *TextBlock `json:"heading_2"`
	Heading3    *TextBlock `json:"heading_3"`
	Bulleted    *TextBlock `json:"bulleted_list_item"`
	Numbered    *TextBlock `json:"numbered_list_item"`
	ToDo        *ToDoBlock `json:"to_do"`
	Quote       *TextBlock `json:"quote"`
	Code        *TextBlock `json:"code"`
}

type TextBlock struct {
	RichText []RichText `json:"rich_text"`
}

type ToDoBlock struct {
	RichText []RichText `json:"rich_text"`
	Checked  bool       `json:"checked"`
}

func (b Block) text() string {
	switch b.Type {
	case "paragraph":
		return blockText(b.Paragraph)
	case "heading_1":
		return blockText(b.Heading1)
	case "heading_2":
		return blockText(b.Heading2)
	case "heading_3":
		return blockText(b.Heading3)
	case "bulleted_list_item":
		return blockText(b.Bulleted)
	case "numbered_list_item":
		return blockText(b.Numbered)
	case "to_do":
		if b.ToDo == nil {
			return ""
		}
		return richTextText(b.ToDo.RichText)
	case "quote":
		return blockText(b.Quote)
	case "code":
		return blockText(b.Code)
	}
	return ""
}

func blockText(b *TextBlock) string {
	if b == nil {
		return ""
	}
	return richTextText(b.RichText)
}

func decode[T any](raw json.RawMessage) (T, error) {
	var v T
	err := json.Unmarshal(raw, &v)
	return v, err
}
