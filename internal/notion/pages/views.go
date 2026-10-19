package pages

import (
	"html/template"
	"net/http"

	"github.com/jeiang/atrium/internal/assistant/projects"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/py"
)

type crumb struct {
	ID    *string `json:"id"`
	Title string  `json:"title"`
}

type property struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type pageView struct {
	ID         string        `json:"id"`
	Title      string        `json:"title"`
	URL        string        `json:"url"`
	Path       []crumb       `json:"path"`
	Private    bool          `json:"private"`
	InTrash    bool          `json:"in_trash"`
	EditedTS   float64       `json:"edited_ts"`
	Properties []property    `json:"properties"`
	BodyHTML   template.HTML `json:"body_html"`
}

type related struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	URL     string  `json:"url"`
	Path    []crumb `json:"path"`
	Private bool    `json:"private"`
	Linked  bool    `json:"linked"`
	Score   float64 `json:"score"`
}

type backLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

type pageData struct {
	NotionPage pageView  `json:"notion_page"`
	Related    []related `json:"related"`
	Back       backLink  `json:"back"`
}

type articleData struct {
	NotionPage pageView `json:"notion_page"`
}

type created struct {
	ID    int64  `json:"id"`
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

type actionView struct {
	Error   string   `json:"error"`
	Created *created `json:"created"`
}

type hubView struct {
	Project *projects.Project `json:"project"`
	HubID   string            `json:"hub_id"`
	Notice  *string           `json:"notice"`
	Error   *string           `json:"error"`
}

func crumbs(path []notion.PathPart) []crumb {
	out := make([]crumb, len(path))
	for i, part := range path {
		out[i] = crumb{ID: part.ID, Title: part.Title}
	}
	return out
}

func newPageView(v *notion.View, body template.HTML) pageView {
	rows := make([]property, len(v.Properties))
	for i, row := range v.Properties {
		rows[i] = property{Name: row.Name, Value: row.Value}
	}
	return pageView{
		ID: v.ID, Title: v.Title, URL: v.URL, Path: crumbs(v.Path), Private: v.Private, InTrash: v.InTrash,
		EditedTS: v.EditedTS, Properties: rows, BodyHTML: body,
	}
}

func newRelated(found []notion.Related) []related {
	out := make([]related, len(found))
	for i, r := range found {
		out[i] = related{
			ID: r.ID, Title: r.Title, URL: r.URL, Path: crumbs(r.Path), Private: r.Private, Linked: r.Linked, Score: r.Score,
		}
	}
	return out
}

var backLabels = map[string]string{"/chat": "Chat", "/triage": "Decisions"}

func backTo(r *http.Request) backLink {
	parts, err := py.URLSplit(r.Header.Get("Referer"))
	if label, ok := backLabels[parts.Path]; ok && err == nil && parts.Netloc == r.Host {
		url := parts.Path
		if parts.Query != "" {
			url += "?" + parts.Query
		}
		return backLink{URL: url, Label: label}
	}
	return backLink{URL: "/chat", Label: "Chat"}
}
