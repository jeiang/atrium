package pages

import (
	"context"
	"strconv"
	"time"

	asstapp "github.com/jeiang/atrium/internal/assistant/application"
	"github.com/jeiang/atrium/internal/assistant/threads"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/web/tpl"
)

const (
	sidebarThreads    = 50
	sidebarPerProject = 5
)

func (p *pages) sidebar(ctx context.Context, f *tpl.Frame) error {
	read, err := p.assistant().ThreadList(ctx, asstapp.ThreadListInput{Limit: sidebarThreads})
	if err != nil {
		return err
	}
	listed := read.Data.Items
	shelved, err := p.assistant().ProjectList(ctx, asstapp.ProjectListInput{Archived: true})
	if err != nil {
		return err
	}
	loc, err := config.From(ctx).Location(p.deps.Env)
	if err != nil {
		return err
	}
	hidden := make(map[int64]bool, len(shelved.Data))
	for _, project := range shelved.Data {
		hidden[project.ID] = true
	}
	f.Threads = sidebarOf(listed, hidden, time.Unix(int64(p.now()), 0).In(loc))
	return nil
}

func sidebarOf(listed []threads.Listed, hidden map[int64]bool, now time.Time) *tpl.SidebarThreads {
	out := &tpl.SidebarThreads{}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	bounds := []struct {
		label string
		from  time.Time
	}{
		{"Today", today},
		{"Yesterday", today.AddDate(0, 0, -1)},
		{"Previous 7 days", today.AddDate(0, 0, -7)},
		{"Previous 30 days", today.AddDate(0, 0, -30)},
		{"Older", time.Time{}},
	}
	groups := map[int64]int{}
	for _, l := range listed {
		if l.ProjectID != nil && hidden[*l.ProjectID] {
			continue
		}
		item := tpl.SidebarThread{URL: "/chat/" + strconv.FormatInt(l.ID, 10), Title: "Untitled thread", Running: l.RunState != nil && *l.RunState == "running"}
		if l.Title != nil && *l.Title != "" {
			item.Title = *l.Title
		}
		switch {
		case bool(l.Pinned):
			out.Pinned = append(out.Pinned, item)
		case l.ProjectID != nil:
			at, ok := groups[*l.ProjectID]
			if !ok {
				name := "Project"
				if l.ProjectName != nil {
					name = *l.ProjectName
				}
				at = len(out.Projects)
				groups[*l.ProjectID] = at
				out.Projects = append(out.Projects, tpl.SidebarGroup{Label: name, URL: "/projects/" + strconv.FormatInt(*l.ProjectID, 10) + "/edit"})
			}
			if len(out.Projects[at].Threads) < sidebarPerProject {
				out.Projects[at].Threads = append(out.Projects[at].Threads, item)
			}
		default:
			updated := time.Unix(int64(l.UpdatedAt), 0).In(now.Location())
			for _, b := range bounds {
				if !updated.Before(b.from) {
					if n := len(out.Dated); n == 0 || out.Dated[n-1].Label != b.label {
						out.Dated = append(out.Dated, tpl.SidebarGroup{Label: b.label})
					}
					out.Dated[len(out.Dated)-1].Threads = append(out.Dated[len(out.Dated)-1].Threads, item)
					break
				}
			}
		}
	}
	return out
}
