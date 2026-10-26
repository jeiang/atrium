package toolset

import (
	calendartools "github.com/jeiang/atrium/internal/assistant/tools/calendar"
	mediatools "github.com/jeiang/atrium/internal/assistant/tools/media"
	notiontools "github.com/jeiang/atrium/internal/assistant/tools/notion"
	profiletools "github.com/jeiang/atrium/internal/assistant/tools/profile"
	"github.com/jeiang/atrium/internal/assistant/writes"
	"github.com/jeiang/atrium/internal/config"
)

func Undoers(env config.Env) *writes.Registry {
	r := writes.NewRegistry()
	profiletools.RegisterUndoer(r)
	calendartools.RegisterUndoers(r)
	mediatools.RegisterUndoer(r, env)
	notiontools.RegisterUndoers(r, env)
	return r
}
