package pages

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strconv"
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
	Cut          bool        `json:"cut"`
}

type SkillDraft struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

type SkillsData struct {
	Skills      []SkillView `json:"skills"`
	Q           string      `json:"q"`
	Draft       SkillDraft  `json:"draft"`
	Editing     string      `json:"editing"`
	Error       string      `json:"error"`
	Focus       string      `json:"focus"`
	StripParams bool        `json:"strip_params"`
	Cut         []string    `json:"cut"`
	Budget      int         `json:"budget"`
}

func skillView(l skills.Listed) SkillView {
	return SkillView{Name: l.Name, Description: l.Description, Body: l.Body, References: l.References, ReferencedBy: l.ReferencedBy}
}

func (p *pages) skillsPage(w http.ResponseWriter, r *http.Request, status int, toast *tpl.Toast, draft SkillDraft, q, editing, formError, focus string, stripParams bool) {
	found, err := skills.List(r.Context(), p.deps.DB.Read, q)
	check(err)
	listed, err := skills.WithReferences(r.Context(), p.deps.DB.Read, found)
	check(err)
	index, err := skills.BuildIndex(r.Context(), p.deps.DB.Read)
	check(err)
	cutSet := map[string]bool{}
	for _, n := range index.Omitted {
		cutSet[n] = true
	}
	views := make([]SkillView, len(listed))
	for i, l := range listed {
		views[i] = skillView(l)
		views[i].Cut = cutSet[l.Name]
	}
	p.settings(w, r, status, "skills.gohtml", SkillsData{
		Skills: views, Q: q, Draft: draft, Editing: editing, Error: formError, Focus: focus, StripParams: stripParams, Cut: index.Omitted, Budget: index.Budget,
	}, toast)
}

func (p *pages) skillsIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := py.Strip(web.QueryValue(r.URL.Query(), "q"))
	var toast *tpl.Toast
	var focus string
	saved := web.QueryValue(r.URL.Query(), "saved")
	if saved != "" {
		skill, err := skills.Get(ctx, p.deps.DB.Read, saved)
		check(err)
		if skill != nil {
			focus = saved
			refs, err := skills.ReferencesOf(ctx, p.deps.DB.Read, saved)
			check(err)
			description := saved
			category := "success"
			if len(refs.Unknown) > 0 {
				category = "warning"
				description += " references skills that do not exist: " + strings.Join(refs.Unknown, ", ")
			}
			toast = notice(category, "Skill saved", description)
		}
	}
	if created, updated := web.QueryValue(r.URL.Query(), "created"), web.QueryValue(r.URL.Query(), "updated"); created != "" || updated != "" {
		toast = notice("success", "Skills imported", created+" created, "+updated+" updated")
	}
	deleted := web.QueryValue(r.URL.Query(), "deleted")
	if toast == nil && deleted != "" {
		skill, err := skills.Get(ctx, p.deps.DB.Read, deleted)
		check(err)
		if skill == nil {
			toast = notice("success", "Skill deleted", deleted)
		}
	}
	p.skillsPage(w, r, http.StatusOK, toast, SkillDraft{}, q, "", "", focus, toast != nil)
}

func (p *pages) skillSave(w http.ResponseWriter, r *http.Request) {
	form := web.FormValues(r)
	draft := SkillDraft{Name: web.FormString(form, "name", ""), Description: web.FormString(form, "description", ""), Body: web.FormString(form, "body", "")}
	ctx := detached(r)
	err := skills.Save(ctx, p.deps.DB.Write, draft.Name, draft.Description, draft.Body, p.now())
	var refused skills.Error
	if errors.As(err, &refused) {
		editing := ""
		if web.FormString(form, "from", "") == "edit" {
			existing, getErr := skills.Get(ctx, p.deps.DB.Read, py.Strip(draft.Name))
			check(getErr)
			if existing != nil {
				editing = existing.Name
			}
		}
		p.skillsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not saved", err.Error()), draft, "", editing, err.Error(), "", false)
		return
	}
	check(err)
	saved := py.Strip(draft.Name)
	settingsRedirect(w, "/settings/skills?saved="+url.QueryEscape(saved))
}

func (p *pages) skillDelete(w http.ResponseWriter, r *http.Request) {
	name := web.PathParam(r, "name")
	removed, err := skills.Delete(detached(r), p.deps.DB.Write, name)
	check(err)
	if !removed {
		p.fail(w, r, http.StatusNotFound, "unknown skill")
		return
	}
	settingsRedirect(w, "/settings/skills?deleted="+url.QueryEscape(name))
}

func (p *pages) skillsExport(w http.ResponseWriter, r *http.Request) {
	names := r.URL.Query()["name"]
	list, err := skills.Export(r.Context(), p.deps.DB.Read, names)
	var refused skills.Error
	if errors.As(err, &refused) {
		p.fail(w, r, http.StatusNotFound, refused.Error())
		return
	}
	check(err)
	var out bytes.Buffer
	check(skills.WriteZip(&out, list))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="atrium-skills.zip"`)
	_, _ = w.Write(out.Bytes())
}

func (p *pages) skillsImport(w http.ResponseWriter, r *http.Request) {
	web.FormValues(r)
	var headers []*multipart.FileHeader
	if r.MultipartForm != nil {
		headers = r.MultipartForm.File["upload"]
	}
	if len(headers) == 0 {
		p.skillsImportFailed(w, r, "choose a SKILL.md file or a zip archive")
		return
	}
	file, err := headers[0].Open()
	check(err)
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(file)
	check(err)
	var items []skills.Item
	if strings.EqualFold(path.Ext(headers[0].Filename), ".zip") {
		items, err = skills.CollectZip(data)
	} else {
		items, err = skills.CollectFile(headers[0].Filename, data)
	}
	var refused skills.Error
	if errors.As(err, &refused) {
		p.skillsImportFailed(w, r, refused.Error())
		return
	}
	check(err)
	done, err := skills.Import(detached(r), p.deps.DB, items, p.now())
	if errors.As(err, &refused) {
		p.skillsImportFailed(w, r, refused.Error())
		return
	}
	check(err)
	location := "/settings/skills?created=" + strconv.Itoa(len(done.Created)) + "&updated=" + strconv.Itoa(len(done.Updated))
	if web.IsPartial(r) {
		hxRedirect(w, location)
		return
	}
	settingsRedirect(w, location)
}

func (p *pages) skillsImportFailed(w http.ResponseWriter, r *http.Request, detail string) {
	if web.IsPartial(r) {
		p.failure(w, r, "Not imported", detail)
		return
	}
	p.skillsPage(w, r, http.StatusUnprocessableEntity, notice("error", "Not imported", detail), SkillDraft{}, "", "", "", "", false)
}
