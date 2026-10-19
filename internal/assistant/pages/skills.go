package pages

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/skills"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type SkillView struct {
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Body         string      `json:"body"`
	References   skills.Refs `json:"references"`
	ReferencedBy []string    `json:"referenced_by"`
}

type SkillDraft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type SkillsData struct {
	Skills []SkillView `json:"skills"`
	Q      string      `json:"q"`
	Draft  SkillDraft  `json:"draft"`
}

func skillView(l skills.Listed) SkillView {
	return SkillView{Name: l.Name, Description: l.Description, Body: l.Body, References: l.References, ReferencedBy: l.ReferencedBy}
}

func (p *pages) skillsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, draft SkillDraft, q string) {
	found, err := skills.List(r.Context(), p.deps.DB.Read, q)
	check(err)
	listed, err := skills.WithReferences(r.Context(), p.deps.DB.Read, found)
	check(err)
	views := make([]SkillView, len(listed))
	for i, l := range listed {
		views[i] = skillView(l)
	}
	p.settings(w, r, status, "skills.gohtml", SkillsData{Skills: views, Q: q, Draft: draft}, toast)
}

func (p *pages) skillsIndex(w http.ResponseWriter, r *http.Request) {
	p.skillsPage(w, r, http.StatusOK, nil, SkillDraft{}, py.Strip(web.QueryValue(r.URL.Query(), "q")))
}

func (p *pages) skillSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	draft := SkillDraft{Name: web.FormString(form, "name", ""), Description: web.FormString(form, "description", ""), Body: web.FormString(form, "body", "")}
	ctx := detached(r)
	err := skills.Save(ctx, p.deps.DB.Write, draft.Name, draft.Description, draft.Body, p.now())
	var refused skills.Error
	if errors.As(err, &refused) {
		p.skillsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), draft, "")
		return
	}
	check(err)
	saved := py.Strip(draft.Name)
	refs, err := skills.ReferencesOf(ctx, p.deps.DB.Read, saved)
	check(err)
	if len(refs.Unknown) > 0 {
		p.skillsPage(w, r, http.StatusOK, notice("warning", "Skill saved", saved+" references skills that do not exist: "+strings.Join(refs.Unknown, ", ")), SkillDraft{}, "")
		return
	}
	p.skillsPage(w, r, http.StatusOK, notice("success", "Skill saved", saved), SkillDraft{}, "")
}

func (p *pages) skillDelete(w http.ResponseWriter, r *http.Request) {
	name := web.PathParam(r, "name")
	removed, err := skills.Delete(detached(r), p.deps.DB.Write, name)
	check(err)
	if !removed {
		p.fail(w, r, http.StatusNotFound, "unknown skill")
		return
	}
	p.skillsPage(w, r, http.StatusOK, notice("success", "Skill deleted", name), SkillDraft{}, "")
}
