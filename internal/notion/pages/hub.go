package pages

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jeiang/atrium/internal/assistant/projects"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	"github.com/jeiang/atrium/internal/assistant/toolset"
	"github.com/jeiang/atrium/internal/notion"
	"github.com/jeiang/atrium/internal/tool"
	"github.com/jeiang/atrium/internal/web"
)

const hubCreated = "Wiki hub created under Wiki Home."

func pathID(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := web.PathParam(r, name)
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		web.Unprocessable(w, web.BadInt("path", name, raw))
		return 0, false
	}
	return id, true
}

func refusal(err error) (string, bool) {
	if text, ok := tool.Message(err); ok {
		return text, true
	}
	var failure *notion.Error
	if errors.As(err, &failure) {
		return failure.Error(), true
	}
	var wiki notion.WikiError
	if errors.As(err, &wiki) {
		return wiki.Error(), true
	}
	return "", false
}

func (s *service) wikiHub(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "project_id")
	if !ok {
		return
	}
	ctx := detached(r)
	project, err := projects.Get(ctx, s.deps.DB.Read, id)
	check(err)
	if project == nil {
		s.fail(w, r, http.StatusNotFound, "unknown project")
		return
	}
	if project.Archived {
		s.fail(w, r, http.StatusUnprocessableEntity, "project is archived")
		return
	}
	if project.WikiHubID != nil && *project.WikiHubID != "" {
		s.fragment(w, r, http.StatusOK, "_project_hub.gohtml", hubView{Project: project, HubID: *project.WikiHubID})
		return
	}
	tc := &tool.Context{
		DB: s.deps.DB, Cfg: s.deps.Cfg, Env: s.deps.Env, Kind: tool.Chat, AllowedURLs: map[string]struct{}{}, Now: s.now,
		Undoers: toolset.Undoers(s.deps.Env),
	}
	made, err := notiontools.CreatePage(ctx, tc, tool.Args{
		"title":    project.Name,
		"markdown": projects.HubMarkdown(*project),
		"sources":  []any{fmt.Sprintf("atrium project %d", id)},
	})
	if reason, refused := refusal(err); refused {
		s.fragment(w, r, http.StatusUnprocessableEntity, "_project_hub.gohtml", hubView{Project: project, Error: &reason})
		return
	}
	check(err)
	page, _ := made.(map[string]any)
	pageID, _ := page["page_id"].(string)
	_, err = projects.SetHub(ctx, s.deps.DB.Write, id, pageID, s.now())
	check(err)
	project, err = projects.Get(ctx, s.deps.DB.Read, id)
	check(err)
	notice := hubCreated
	s.fragment(w, r, http.StatusOK, "_project_hub.gohtml", hubView{Project: project, HubID: pageID, Notice: &notice})
}
