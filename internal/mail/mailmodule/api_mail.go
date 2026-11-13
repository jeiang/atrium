package mailmodule

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"

	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/mail"
	mailapp "github.com/jeiang/atrium/internal/mail/application"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/mail/live"
	"github.com/jeiang/atrium/internal/mail/message"
	"github.com/jeiang/atrium/internal/mail/ruleedit"
	"github.com/jeiang/atrium/internal/mail/rules"
	"github.com/jeiang/atrium/internal/mail/search"
	mailsync "github.com/jeiang/atrium/internal/mail/sync"
	"github.com/jeiang/atrium/internal/mail/triage"
	"github.com/jeiang/atrium/internal/module"
	"github.com/jeiang/atrium/internal/web"
)

type MailListRequest struct {
	Account string     `query:"account" json:"-"`
	Cursor  api.Cursor `query:"cursor" json:"-"`
	Limit   api.Limit  `query:"limit" json:"-"`
}
type MailTriageRequest struct {
	Account   string     `query:"account" json:"-"`
	Stage     string     `query:"stage" json:"-"`
	Agreement string     `query:"agreement" json:"-"`
	Q         string     `query:"q" json:"-"`
	Cursor    api.Cursor `query:"cursor" json:"-"`
	Limit     api.Limit  `query:"limit" json:"-"`
}
type MailTriagePage struct {
	Items      []triage.Row `json:"items"`
	Check      []triage.Row `json:"check"`
	Total      int          `json:"total"`
	NextCursor *string      `json:"next_cursor" api:"nullable"`
}
type MailMessageRef struct {
	ID int64 `path:"message_id" json:"-" api:"min=1"`
}
type MailMessage struct {
	ID           int64                      `json:"id"`
	Account      string                     `json:"account"`
	Headers      map[string]string          `json:"headers"`
	DateTS       float64                    `json:"date_ts"`
	FromAddr     string                     `json:"from_addr"`
	FromName     string                     `json:"from_name"`
	Subject      string                     `json:"subject"`
	Body         string                     `json:"body"`
	BodySource   string                     `json:"body_source"`
	Attachments  []message.Attachment       `json:"attachments"`
	Location     string                     `json:"location"`
	Folder       string                     `json:"folder"`
	CorrectedTo  *string                    `json:"corrected_to" api:"nullable"`
	InInbox      bool                       `json:"in_inbox"`
	Seen         bool                       `json:"seen"`
	Flagged      bool                       `json:"flagged"`
	Decision     map[string]json.RawMessage `json:"decision"`
	FlagDecision map[string]json.RawMessage `json:"flag_decision"`
}
type MailSearchRequest struct {
	Q             string     `query:"q" json:"-"`
	Account       string     `query:"account" json:"-"`
	Folder        string     `query:"folder" json:"-"`
	From          string     `query:"from" json:"-"`
	Unread        bool       `query:"unread" json:"-"`
	Flagged       bool       `query:"flagged" json:"-"`
	HasAttachment bool       `query:"has_attachment" json:"-"`
	DateFrom      *float64   `query:"date_from" json:"-"`
	DateTo        *float64   `query:"date_to" json:"-"`
	Cursor        api.Cursor `query:"cursor" json:"-"`
	Limit         api.Limit  `query:"limit" json:"-"`
}
type MailSearchHit struct {
	ID      int64           `json:"message_id"`
	Account string          `json:"account"`
	Folder  string          `json:"folder"`
	Date    string          `json:"date"`
	From    string          `json:"from"`
	To      json.RawMessage `json:"to"`
	Subject string          `json:"subject"`
	Snippet string          `json:"snippet"`
}
type MailSearchPage struct {
	Items      []MailSearchHit `json:"items"`
	Semantic   bool            `json:"semantic"`
	NextCursor *string         `json:"next_cursor" api:"nullable"`
}
type MailCondition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}
type MailRule struct {
	ID      string          `json:"id,omitempty"`
	Account *string         `json:"account,omitempty"`
	Action  string          `json:"action" api:"enum=file|delete|none"`
	Dest    *string         `json:"dest,omitempty"`
	When    []MailCondition `json:"when"`
}
type MailRuleRequest struct {
	Rule MailRule `json:"rule"`
}
type MailRuleUpdate struct {
	ID   string   `path:"rule_id" json:"-"`
	Rule MailRule `json:"rule"`
}
type MailRuleRef struct {
	ID string `path:"rule_id" json:"-"`
}
type MailRuleOrder struct {
	ID        string `json:"id"`
	Account   string `json:"account,omitempty"`
	Direction string `json:"direction" api:"enum=up|down"`
}
type MailRulesPage struct {
	Items      []MailRule `json:"items"`
	NextCursor *string    `json:"next_cursor" api:"nullable"`
}
type MailRulesDocument struct {
	Rules []MailRule `json:"rules"`
}
type MailRulesImport struct {
	Document json.RawMessage `json:"-"`
}

func (in *MailRulesImport) DecodeMultipart(r *multipart.Reader) error {
	part, err := r.NextPart()
	if err != nil {
		return err
	}
	defer func() { _ = part.Close() }()
	if part.FormName() != "upload" {
		return fmt.Errorf("expected upload")
	}
	dec := json.NewDecoder(part)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in.Document); err != nil {
		return err
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected one rules document")
	}
	if _, err := r.NextPart(); err != io.EOF {
		return fmt.Errorf("expected one file")
	}
	return nil
}

type MailCorrection struct {
	ID     int64               `path:"message_id" json:"-" api:"min=1"`
	Folder string              `json:"folder"`
	Mode   mailapp.CorrectMode `json:"mode" api:"enum=immediate|deferred"`
}
type MailAccountAdd struct {
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	Preset     string `json:"preset" api:"enum=icloud|gmail"`
	Login      string `json:"login"`
	AuthMethod string `json:"auth_method,omitempty" api:"enum=password|xoauth2"`
	Password   string `json:"password,omitempty" api:"writeonly"`
}
type MailAccountUpdate struct {
	ID         string  `path:"account_id" json:"-"`
	Label      *string `json:"label,omitempty"`
	Login      *string `json:"login,omitempty"`
	AuthMethod *string `json:"auth_method,omitempty" api:"enum=password|xoauth2"`
	Password   string  `json:"password,omitempty" api:"writeonly"`
	Enabled    bool    `json:"enabled"`
}
type MailAccountRef struct {
	ID string `path:"account_id" json:"-"`
}
type MailAccountResult struct {
	ID string `json:"id"`
}
type MailAccountTest struct {
	OK     bool   `json:"ok"`
	Reason string `json:"reason"`
}
type MailAccountsPage struct {
	Items      []mailapp.Account `json:"items"`
	NextCursor *string           `json:"next_cursor" api:"nullable"`
}
type MailActionsRequest struct {
	Kind   string     `query:"kind" json:"-"`
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}
type MailActionsPage struct {
	Items      []live.Action `json:"items"`
	NextCursor *string       `json:"next_cursor" api:"nullable"`
}

func mailWire[T any](v any) (T, error) {
	var out T
	data, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(data, &out)
	}
	return out, err
}
func mailRuleMap(v MailRule) (map[string]any, error) { return mailWire[map[string]any](v) }
func mailOffset(c api.Cursor) (int, error) {
	var n int
	if c.Set() {
		if err := c.Last(&n); err != nil {
			return 0, err
		}
		if n < 0 || n > int(^uint(0)>>1)-101 {
			return 0, &application.Error{Code: application.BadCursor, Message: "Invalid cursor"}
		}
	}
	return n, nil
}
func mailSlice[T any](items []T, c api.Cursor, limit api.Limit) ([]T, *string, error) {
	offset, err := mailOffset(c)
	if err != nil {
		return nil, nil, err
	}
	end := min(len(items), offset+int(limit))
	var next *string
	if end < len(items) {
		next = c.Next(end)
	}
	return append([]T{}, items[min(offset, len(items)):end]...), next, nil
}
func mailRuleChanged(change application.Change[rules.Rule], err error) (application.Change[MailRule], error) {
	out := application.Change[MailRule]{AuditID: change.AuditID}
	if err == nil {
		raw := MailRule{ID: change.Data.ID, Account: change.Data.Account, Action: string(change.Data.Action), Dest: change.Data.Dest, When: []MailCondition{}}
		for _, cond := range change.Data.Conditions {
			v, e := mailWire[MailCondition](cond)
			if e != nil {
				return out, e
			}
			raw.When = append(raw.When, v)
		}
		out.Data = raw
	}
	return out, err
}
func mailAPIRoutes(d *web.Deps) []application.Binding {
	var service *mailapp.Service
	svc := func() *mailapp.Service { return service }
	if d != nil {
		open := imapclient.Opener(d.Cfg)
		syncer := &mailsync.Syncer{DB: d.DB, Cfg: d.Cfg, Now: mail.WallClock}
		cat := module.SetFrom(d.Ctx).Catalog()
		store, err := cat.Ledger(d.DB, d.Env, open, mail.WallClock)
		if err != nil {
			panic(err)
		}
		d.DrainOnStop(store.Drain)
		registry, err := cat.Proposals(module.EffectDeps{DB: d.DB, Ledger: store, Env: d.Env, Open: open, Syncer: syncer, Now: mail.WallClock, Approvals: cat.Approvals()})
		if err != nil {
			panic(err)
		}
		l := &live.Live{DB: d.DB, Cfg: d.Cfg, Store: store, Open: open, Registry: registry, Now: mail.WallClock}
		service = mailPageService(d).WithLive(l).WithApplier(applier.New(l, open, syncer))
	}
	const area = "mail"
	refusable := []application.Code{application.Invalid, application.Refused, application.NotFound, application.Conflict, application.Unknown, application.Unavailable}
	return []application.Binding{
		api.Query(d, mailapp.DescribeTriage(), application.APIShape{Area: area}, "GET /mail/triage", func(ctx context.Context, in MailTriageRequest) (application.Read[MailTriagePage], error) {
			offset, err := mailOffset(in.Cursor)
			if err != nil {
				return application.Read[MailTriagePage]{}, err
			}
			r, err := svc().Triage(ctx, mailapp.TriageInput{Account: in.Account, Stage: in.Stage, Agreement: in.Agreement, Q: in.Q, Offset: offset, Limit: int(in.Limit)})
			page := MailTriagePage{Items: r.Data.Rows, Check: r.Data.Check, Total: r.Data.Total}
			if r.Data.NextOffset != 0 {
				page.NextCursor = in.Cursor.Next(r.Data.NextOffset)
			}
			return application.Read[MailTriagePage]{Revision: r.Revision, Data: page}, err
		}),
		api.Query(d, mailapp.DescribeMessage(), application.APIShape{Area: area, Errors: refusable}, "GET /mail/messages/{message_id}", func(ctx context.Context, in MailMessageRef) (application.Read[MailMessage], error) {
			r, err := svc().Message(ctx, mailapp.MessageInput{ID: in.ID})
			if err == nil && r.Data.View == nil {
				err = &application.Error{Code: application.NotFound, Message: "Unknown message"}
			}
			if err != nil {
				return application.Read[MailMessage]{}, err
			}
			out, err := mailWire[MailMessage](r.Data.View)
			return application.Read[MailMessage]{Revision: r.Revision, Data: out}, err
		}),
		api.Query(d, mailapp.DescribeSearch(), application.APIShape{Area: area}, "GET /mail/messages", func(ctx context.Context, in MailSearchRequest) (application.Read[MailSearchPage], error) {
			offset, err := mailOffset(in.Cursor)
			if err != nil {
				return application.Read[MailSearchPage]{}, err
			}
			r, err := svc().Search(ctx, mailapp.SearchInput{Query: in.Q, Limit: int(in.Limit) + 1, Offset: offset, Filters: search.Filters{Account: search.Text(in.Account), Folder: search.Text(in.Folder), From: search.Text(in.From), Unread: in.Unread, Flagged: in.Flagged, HasAttachment: in.HasAttachment, DateFrom: in.DateFrom, DateTo: in.DateTo}})
			if err != nil {
				return application.Read[MailSearchPage]{}, err
			}
			page := MailSearchPage{Semantic: r.Data.Semantic}
			if len(r.Data.Results) > int(in.Limit) {
				page.NextCursor = in.Cursor.Next(offset + int(in.Limit))
				r.Data.Results = r.Data.Results[:in.Limit]
			}
			page.Items, err = mailWire[[]MailSearchHit](r.Data.Results)
			return application.Read[MailSearchPage]{Revision: r.Revision, Data: page}, err
		}),
		api.Query(d, mailapp.DescribeRules(), application.APIShape{Area: area}, "GET /mail/rules", func(ctx context.Context, in MailListRequest) (application.Read[MailRulesPage], error) {
			r, err := svc().Rules(ctx, mailapp.RulesInput{Account: in.Account})
			if err != nil {
				return application.Read[MailRulesPage]{}, err
			}
			items, err := mailWire[[]MailRule](r.Data.List.Rules)
			if err != nil {
				return application.Read[MailRulesPage]{}, err
			}
			items, next, err := mailSlice(items, in.Cursor, in.Limit)
			return application.Read[MailRulesPage]{Revision: r.Revision, Data: MailRulesPage{Items: items, NextCursor: next}}, err
		}),
		api.Query(d, mailapp.DescribeRulePreview(), application.APIShape{Area: area, Body: application.BodyJSON, ReadOnlyPost: true, Errors: refusable}, "POST /mail/rules/preview", func(ctx context.Context, in MailRuleRequest) (application.Read[ruleedit.Preview], error) {
			raw, err := mailRuleMap(in.Rule)
			if err != nil {
				return application.Read[ruleedit.Preview]{}, err
			}
			return svc().RulePreview(ctx, mailapp.RulePreviewInput{Rule: raw})
		}),
		api.Query(d, mailapp.DescribeRulesExport(), application.APIShape{Area: area}, "GET /mail/rules/export", func(ctx context.Context, in struct{}) (application.Read[MailRulesDocument], error) {
			r, err := svc().RulesExport(ctx, in)
			if err != nil {
				return application.Read[MailRulesDocument]{}, err
			}
			out, err := mailWire[MailRulesDocument](r.Data)
			return application.Read[MailRulesDocument]{Revision: r.Revision, Data: out}, err
		}),
		api.Command(d, mailapp.DescribeRulesImport(), application.APIShape{Area: area, Body: application.BodyMultipart, BodyLimit: 1 << 20, Errors: refusable}, "POST /mail/rules/import", func(ctx context.Context, in MailRulesImport) (application.Change[mailapp.RulesImportResult], error) {
			var raw any
			err := json.Unmarshal(in.Document, &raw)
			if err != nil {
				return application.Change[mailapp.RulesImportResult]{}, err
			}
			return svc().RulesImport(ctx, mailapp.RulesImportInput{Document: raw})
		}),
		api.Command(d, mailapp.DescribeRuleCreate(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable}, "POST /mail/rules", func(ctx context.Context, in MailRuleRequest) (application.Change[MailRule], error) {
			raw, err := mailRuleMap(in.Rule)
			if err != nil {
				return application.Change[MailRule]{}, err
			}
			return mailRuleChanged(svc().RuleCreate(ctx, mailapp.RuleInput{Rule: raw}))
		}),
		api.Command(d, mailapp.DescribeRuleUpdate(), application.APIShape{Area: area, Errors: refusable}, "PATCH /mail/rules/{rule_id}", func(ctx context.Context, in MailRuleUpdate) (application.Change[MailRule], error) {
			raw, err := mailRuleMap(in.Rule)
			if err != nil {
				return application.Change[MailRule]{}, err
			}
			return mailRuleChanged(svc().RuleUpdate(ctx, mailapp.RuleInput{ID: in.ID, Rule: raw}))
		}),
		api.Command(d, mailapp.DescribeRuleReorder(), application.APIShape{Area: area, Errors: refusable}, "PUT /mail/rules/order", func(ctx context.Context, in MailRuleOrder) (application.Change[mailapp.RuleReorderResult], error) {
			return svc().RuleReorder(ctx, mailapp.RuleReorderInput{ID: in.ID, Account: in.Account, Direction: in.Direction})
		}),
		api.Command(d, mailapp.DescribeRuleDelete(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable}, "DELETE /mail/rules/{rule_id}", func(ctx context.Context, in MailRuleRef) (application.Change[mailapp.RuleDeleteResult], error) {
			return svc().RuleDelete(ctx, mailapp.RuleDeleteInput{ID: in.ID})
		}),
		api.Command(d, mailapp.DescribeCorrect(), application.APIShape{Area: area, Errors: refusable}, "POST /mail/messages/{message_id}/correction", func(ctx context.Context, in MailCorrection) (application.Change[mailapp.CorrectResult], error) {
			if in.Mode != mailapp.Immediate && in.Mode != mailapp.Deferred {
				return application.Change[mailapp.CorrectResult]{}, &application.Error{Code: application.Invalid, Message: "mode must be immediate or deferred", Fields: []application.Field{{Name: "mode", Code: "invalid", Message: "Must be exactly immediate or deferred"}}}
			}
			done, err := svc().Correct(ctx, mailapp.CorrectInput{MessageID: in.ID, Folder: in.Folder, Mode: in.Mode})
			if err != nil {
				return done, err
			}
			code := map[string]application.Code{"refused": application.Refused, "unknown": application.Unknown, "failed": application.Conflict}[done.Data.Outcome]
			if code == "" {
				return done, nil
			}
			failure := &application.Error{Code: code, Reason: done.Data.Outcome, Message: strings.TrimSpace(done.Data.Note), EffectIDs: done.EffectIDs}
			if code == application.Refused && len(done.EffectIDs) == 0 {
				failure.Code, failure.RetryAfter = application.Unavailable, busyRetryAfter
			}
			return done, failure
		}),
		api.Query(d, mailapp.DescribeAccounts(), application.APIShape{Area: area}, "GET /mail/accounts", func(ctx context.Context, in MailListRequest) (application.Read[MailAccountsPage], error) {
			r, err := svc().Accounts(ctx, struct{}{})
			if err != nil {
				return application.Read[MailAccountsPage]{}, err
			}
			items := slices.DeleteFunc(r.Data.Accounts, func(a mailapp.Account) bool { return in.Account != "" && a.ID != in.Account })
			items, next, err := mailSlice(items, in.Cursor, in.Limit)
			return application.Read[MailAccountsPage]{Revision: r.Revision, Data: MailAccountsPage{Items: items, NextCursor: next}}, err
		}),
		api.Command(d, mailapp.DescribeAccountAdd(), application.APIShape{Area: area, Success: http.StatusCreated, Errors: refusable}, "POST /mail/accounts", func(ctx context.Context, in MailAccountAdd) (application.Change[MailAccountResult], error) {
			r, err := svc().AccountAdd(ctx, mailapp.AccountAddInput{ID: in.ID, Label: in.Label, Preset: in.Preset, Login: in.Login, AuthMethod: in.AuthMethod, Password: in.Password})
			return application.Change[MailAccountResult]{Data: MailAccountResult{ID: r.Data.ID}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeAccountUpdate(), application.APIShape{Area: area, Errors: refusable}, "PATCH /mail/accounts/{account_id}", func(ctx context.Context, in MailAccountUpdate) (application.Change[MailAccountResult], error) {
			r, err := svc().AccountUpdate(ctx, mailapp.AccountUpdateInput{ID: in.ID, Label: in.Label, Login: in.Login, AuthMethod: in.AuthMethod, Password: in.Password, Enabled: in.Enabled})
			return application.Change[MailAccountResult]{Data: MailAccountResult{ID: r.Data.ID}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeAccountTest(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable}, "POST /mail/accounts/{account_id}/test", func(ctx context.Context, in MailAccountRef) (application.Change[MailAccountTest], error) {
			r, err := svc().AccountTest(ctx, mailapp.AccountInput{ID: in.ID})
			return application.Change[MailAccountTest]{Data: MailAccountTest{OK: r.Data.OK, Reason: r.Data.Reason}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribeAccountRemove(), application.APIShape{Area: area, Body: application.BodyNone, Errors: refusable}, "DELETE /mail/accounts/{account_id}", func(ctx context.Context, in MailAccountRef) (application.Change[MailAccountResult], error) {
			r, err := svc().AccountRemove(ctx, mailapp.AccountInput{ID: in.ID})
			return application.Change[MailAccountResult]{Data: MailAccountResult{ID: r.Data.ID}, AuditID: r.AuditID}, err
		}),
		api.Command(d, mailapp.DescribePause(), application.APIShape{Area: area, Unowned: true}, "POST /mail/live/pause", func(ctx context.Context, in mailapp.PauseInput) (application.Change[mailapp.PauseResult], error) {
			return svc().Pause(ctx, in)
		}),
		api.Command(d, mailapp.DescribeResume(), application.APIShape{Area: area}, "POST /mail/live/resume", func(ctx context.Context, in mailapp.PauseInput) (application.Change[mailapp.PauseResult], error) {
			return svc().Resume(ctx, in)
		}),
		api.Query(d, mailapp.DescribeLiveStatus(), application.APIShape{Area: area}, "GET /mail/live", func(ctx context.Context, in struct{}) (application.Read[live.Status], error) {
			return svc().LiveStatus(ctx, in)
		}),
		api.Query(d, mailapp.DescribeLiveActions(), application.APIShape{Area: area}, "GET /mail/live/actions", func(ctx context.Context, in MailActionsRequest) (application.Read[MailActionsPage], error) {
			offset, err := mailOffset(in.Cursor)
			if err != nil {
				return application.Read[MailActionsPage]{}, err
			}
			r, err := svc().LiveActions(ctx, mailapp.LiveActionsInput{Kind: in.Kind, Limit: int(in.Limit) + 1, Offset: offset})
			page := MailActionsPage{Items: r.Data}
			if len(page.Items) > int(in.Limit) {
				page.Items = page.Items[:in.Limit]
				page.NextCursor = in.Cursor.Next(offset + int(in.Limit))
			}
			return application.Read[MailActionsPage]{Revision: r.Revision, Data: page}, err
		}),
	}
}
