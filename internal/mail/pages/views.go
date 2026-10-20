package pages

import (
	"math/big"
	"net/http"

	"github.com/jeiang/atrium/internal/mail/drafts"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/py"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type backLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

type messageData struct {
	Message   *message.View `json:"message"`
	Back      backLink      `json:"back"`
	FromDraft *drafts.Row   `json:"from_draft"`
	Correct   *correctForm  `json:"correct"`
}

type correctForm struct {
	MessageID int64           `json:"message_id"`
	Account   string          `json:"account"`
	Context   string          `json:"context"`
	Folders   []triage.Folder `json:"folders"`
	Selected  *string         `json:"selected"`
	Error     *string         `json:"error"`
}

type correctData struct {
	Correct *correctForm `json:"correct"`
}

type rowData struct {
	Row     *triage.Row  `json:"row"`
	Correct *correctForm `json:"correct"`
}

type triageData struct {
	Summaries   []triage.Summary  `json:"summaries"`
	Flags       []triage.Judgment `json:"flags"`
	FlagsCapped bool              `json:"flags_capped"`
	Rows        []triage.Row      `json:"rows"`
	Total       int               `json:"total"`
	Offset      int               `json:"offset"`
	NextOffset  int               `json:"next_offset"`
	Account     string            `json:"account"`
	Stage       string            `json:"stage"`
	Agreement   string            `json:"agreement"`
	Q           string            `json:"q"`
}

type ruleView struct {
	ID      string           `json:"id"`
	Account *string          `json:"account"`
	Action  string           `json:"action"`
	Dest    *string          `json:"dest"`
	When    []map[string]any `json:"when"`
}

type learnedView struct {
	Level   string  `json:"level"`
	Key     string  `json:"key"`
	Dest    string  `json:"dest"`
	Purity  float64 `json:"purity"`
	N       int     `json:"n"`
	Account string  `json:"account"`
}

type rulesData struct {
	Account      string        `json:"account"`
	Rules        []ruleView    `json:"rules"`
	Learned      []learnedView `json:"learned"`
	LearnedLimit *int          `json:"learned_limit"`
}

type moved struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
}

type ruleListData struct {
	Rules   []ruleView `json:"rules"`
	Account string     `json:"account"`
	Moved   *moved     `json:"moved"`
	FocusID *string    `json:"focus_id"`
}

type sourceMessage struct {
	ID       int64  `json:"id"`
	Subject  string `json:"subject"`
	FromAddr string `json:"from_addr"`
}

type ruleEditData struct {
	Rule          ruleView                   `json:"rule"`
	IsNew         bool                       `json:"is_new"`
	Folders       map[string][]triage.Folder `json:"folders"`
	Suggestions   []map[string]any           `json:"suggestions"`
	SourceMessage *sourceMessage             `json:"source_message"`
	Errors        []string                   `json:"errors"`
}

type conditionData struct {
	C     map[string]any `json:"c"`
	Focus string         `json:"focus"`
}

func conditionView(c rules.Condition) map[string]any {
	out := map[string]any{"field": c.Field, "op": c.Op}
	if !c.HasValue {
		return out
	}
	if n, ok := c.Value.(*big.Int); ok {
		out["value"] = n.String()
	} else {
		out["value"] = c.Value
	}
	return out
}

func conditionViews(cs []rules.Condition) []map[string]any {
	out := make([]map[string]any, len(cs))
	for i, c := range cs {
		out[i] = conditionView(c)
	}
	return out
}

func toast(category, title, description string) *tpl.Toast {
	return &tpl.Toast{Category: category, Title: title, Description: description}
}

func pathInt(r *http.Request, name string) (int64, []web.FieldError) {
	text := web.PathParam(r, name)
	n, ok := py.LaxInt64(text)
	if !ok {
		return 0, []web.FieldError{web.BadInt("path", name, text)}
	}
	return n, nil
}
