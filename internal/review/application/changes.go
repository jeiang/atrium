package application

import (
	"encoding/json"
	"reflect"
	"sync"

	"github.com/jeiang/atrium/internal/application"
)

const (
	ToolCallChange = "tool_call"
	GuardedChange  = "guarded_change"
)

type ProposalChange interface{ proposalChange() }

type PreviewMessage struct {
	ID       int64   `json:"id"`
	Subject  string  `json:"subject"`
	FromName string  `json:"from_name"`
	FromAddr string  `json:"from_addr"`
	DateTS   float64 `json:"date_ts"`
}

type PreviewCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type PreviewGroup struct {
	Name     string           `json:"name"`
	Count    int              `json:"count"`
	Senders  []PreviewCount   `json:"senders"`
	Terms    []string         `json:"terms"`
	Subjects []string         `json:"subjects"`
	Messages []PreviewMessage `json:"messages"`
}

type PreviewSource struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

type PreviewCondition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value"`
}

type PreviewRule struct {
	Account string             `json:"account"`
	When    []PreviewCondition `json:"when"`
}

type PreviewTriageItem struct {
	ID       int64   `json:"id"`
	Subject  string  `json:"subject"`
	FromAddr string  `json:"from_addr"`
	DateTS   float64 `json:"date_ts"`
	Action   string  `json:"action"`
	Label    string  `json:"label"`
}

type PreviewFolderSplit struct {
	Kind   string         `json:"kind"`
	Folder string         `json:"folder"`
	Groups []PreviewGroup `json:"groups"`
}

type PreviewInboxBacklog struct {
	Kind   string         `json:"kind"`
	Folder string         `json:"folder"`
	Groups []PreviewGroup `json:"groups"`
}

type PreviewFolderMerge struct {
	Kind    string          `json:"kind"`
	Folder  string          `json:"folder"`
	Sources []PreviewSource `json:"sources"`
	Into    string          `json:"into"`
	Group   PreviewGroup    `json:"group"`
}

type PreviewDeletionRule struct {
	Kind     string           `json:"kind"`
	Reason   string           `json:"reason"`
	Rule     PreviewRule      `json:"rule"`
	Backlog  int              `json:"backlog"`
	Subjects []string         `json:"subjects"`
	Senders  []PreviewCount   `json:"senders"`
	Messages []PreviewMessage `json:"messages"`
}

type PreviewLabelMerge struct {
	Kind      string           `json:"kind"`
	From      string           `json:"from"`
	Into      string           `json:"into"`
	FromCount int              `json:"from_count"`
	IntoCount int              `json:"into_count"`
	Messages  []PreviewMessage `json:"messages"`
}

type PreviewMailTriage struct {
	Kind     string              `json:"kind"`
	Count    int                 `json:"count"`
	Messages []PreviewTriageItem `json:"messages"`
}

type PreviewCalendarEvent struct {
	Kind     string          `json:"kind"`
	Calendar string          `json:"calendar"`
	Title    string          `json:"title"`
	Start    string          `json:"start"`
	End      string          `json:"end"`
	AllDay   bool            `json:"all_day"`
	Location string          `json:"location"`
	Notes    string          `json:"notes"`
	Source   json.RawMessage `json:"source"`
}

type PreviewCalendarUpdate struct {
	Kind     string `json:"kind"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	Title    string `json:"title"`
	Start    string `json:"start"`
	End      string `json:"end"`
	Location string `json:"location"`
}

type PreviewCalendarReply struct {
	Kind     string `json:"kind"`
	Calendar string `json:"calendar"`
	UID      string `json:"uid"`
	Response string `json:"response"`
}

type PreviewReminder struct {
	Kind         string `json:"kind"`
	CommitmentID int64  `json:"commitment_id"`
	Text         string `json:"text"`
	Due          string `json:"due"`
	Owner        string `json:"owner"`
	MessageID    int64  `json:"message_id"`
	TZ           string `json:"tz"`
	RemindAt     string `json:"remind_at"`
	Subject      string `json:"subject"`
}

type PreviewToolCall struct {
	Kind string          `json:"kind"`
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
}

type PreviewGuardedChange struct {
	Kind    string               `json:"kind"`
	Title   string               `json:"title"`
	Subject string               `json:"subject"`
	Rule    string               `json:"rule"`
	Fields  []PreviewChangeField `json:"fields"`
	Note    string               `json:"note"`
}

type PreviewChangeField struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

func (PreviewFolderSplit) proposalChange()    {}
func (PreviewInboxBacklog) proposalChange()   {}
func (PreviewFolderMerge) proposalChange()    {}
func (PreviewDeletionRule) proposalChange()   {}
func (PreviewLabelMerge) proposalChange()     {}
func (PreviewMailTriage) proposalChange()     {}
func (PreviewCalendarEvent) proposalChange()  {}
func (PreviewCalendarUpdate) proposalChange() {}
func (PreviewCalendarReply) proposalChange()  {}
func (PreviewReminder) proposalChange()       {}
func (PreviewToolCall) proposalChange()       {}
func (PreviewGuardedChange) proposalChange()  {}

var changeKinds = map[string]reflect.Type{
	"rebalance_split":   reflect.TypeFor[PreviewFolderSplit](),
	"inbox_backlog":     reflect.TypeFor[PreviewInboxBacklog](),
	"rebalance_merge":   reflect.TypeFor[PreviewFolderMerge](),
	"deletion_rule":     reflect.TypeFor[PreviewDeletionRule](),
	"gmail_label_merge": reflect.TypeFor[PreviewLabelMerge](),
	"mail_triage":       reflect.TypeFor[PreviewMailTriage](),
	"calendar_event":    reflect.TypeFor[PreviewCalendarEvent](),
	"calendar_update":   reflect.TypeFor[PreviewCalendarUpdate](),
	"calendar_rsvp":     reflect.TypeFor[PreviewCalendarReply](),
	"reminder":          reflect.TypeFor[PreviewReminder](),
	ToolCallChange:      reflect.TypeFor[PreviewToolCall](),
	GuardedChange:       reflect.TypeFor[PreviewGuardedChange](),
}

var RegisterChanges = sync.OnceFunc(func() {
	application.RegisterUnion[ProposalChange](changeKinds)
})

func ChangeOf(c Card) ProposalChange {
	if c.Approval {
		args := struct {
			Args json.RawMessage `json:"args"`
		}{}
		raw, err := json.Marshal(c.Preview)
		if err != nil || json.Unmarshal(raw, &args) != nil {
			return nil
		}
		return PreviewToolCall{Kind: ToolCallChange, Tool: c.Kind, Args: args.Args}
	}
	t, ok := changeKinds[c.Kind]
	if !ok || c.Kind == ToolCallChange || c.Kind == GuardedChange {
		return nil
	}
	raw, err := json.Marshal(c.Preview)
	if err != nil {
		return nil
	}
	v := reflect.New(t)
	if err := json.Unmarshal(raw, v.Interface()); err != nil {
		return nil
	}
	v.Elem().FieldByName("Kind").SetString(c.Kind)
	return v.Elem().Interface().(ProposalChange)
}

func GuardedChangeOf(c Card) ProposalChange {
	var preview struct {
		Change *PreviewGuardedChange `json:"change"`
	}
	raw, err := json.Marshal(c.Preview)
	if err != nil || json.Unmarshal(raw, &preview) != nil || preview.Change == nil {
		return nil
	}
	preview.Change.Kind = GuardedChange
	if preview.Change.Fields == nil {
		preview.Change.Fields = []PreviewChangeField{}
	}
	return *preview.Change
}
