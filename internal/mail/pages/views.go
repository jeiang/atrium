package pages

import (
	"net/http"
	"strconv"
	"strings"

	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/web"
	"github.com/jeiang/atrium/internal/web/tpl"
)

type backLink struct {
	URL   string `json:"url"`
	Label string `json:"label"`
}

func laxInt64(s string) (int64, bool) {
	body := strings.TrimSpace(s)
	neg := false
	if rest, ok := strings.CutPrefix(body, "-"); ok {
		body, neg = rest, true
	} else if rest, ok := strings.CutPrefix(body, "+"); ok {
		body = rest
	}
	whole, fraction, hasFraction := strings.Cut(body, ".")
	if hasFraction && (fraction == "" || strings.Trim(fraction, "0") != "") {
		return 0, false
	}
	if whole == "" || strings.HasPrefix(whole, "_") || strings.HasSuffix(whole, "_") || strings.Contains(whole, "__") {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.ReplaceAll(whole, "_", ""), 10, 64)
	if err != nil {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

type messageData struct {
	Message   *mailapp.MessageView `json:"message"`
	Back      backLink             `json:"back"`
	FromDraft *mailapp.DraftRow    `json:"from_draft"`
	Correct   *correctForm         `json:"correct"`
}

type correctForm struct {
	MessageID int64                  `json:"message_id"`
	Account   string                 `json:"account"`
	Context   string                 `json:"context"`
	Folders   []mailapp.TriageFolder `json:"folders"`
	Selected  *string                `json:"selected"`
	Error     *string                `json:"error"`
}

type correctData struct {
	Correct *correctForm `json:"correct"`
}

type rowData struct {
	Row     *mailapp.TriageRow `json:"row"`
	Correct *correctForm       `json:"correct"`
}

type moved struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
}

type ruleListData struct {
	Rules   []mailapp.RuleView `json:"rules"`
	Account string             `json:"account"`
	Moved   *moved             `json:"moved"`
	FocusID *string            `json:"focus_id"`
}

func toast(category, title, description string) *tpl.Toast {
	return &tpl.Toast{Category: category, Title: title, Description: description}
}

func pathInt(r *http.Request, name string) (int64, []web.FieldError) {
	text := web.PathParam(r, name)
	n, ok := laxInt64(text)
	if !ok {
		return 0, []web.FieldError{web.BadInt("path", name, text)}
	}
	return n, nil
}
