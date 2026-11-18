package tool

import "context"

type Change struct {
	Title   string        `json:"title"`
	Subject string        `json:"subject"`
	Rule    string        `json:"rule"`
	Fields  []ChangeField `json:"fields"`
	Note    string        `json:"note,omitempty"`
}

type ChangeField struct {
	Name   string `json:"name"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type NeedsApproval struct{ Change Change }

func (e *NeedsApproval) Error() string {
	return e.Change.Rule + "; this needs the operator's approval, which only a chat run can ask for"
}

type approvedKey struct{}

func WithApproved(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvedKey{}, true)
}

func Approved(ctx context.Context) bool {
	ok, _ := ctx.Value(approvedKey{}).(bool)
	return ok
}
