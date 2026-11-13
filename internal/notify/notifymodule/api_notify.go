package notifymodule

import (
	"context"
	"net/http"
	"reflect"

	"github.com/jeiang/atrium/internal/actionlink/policy"
	"github.com/jeiang/atrium/internal/api"
	"github.com/jeiang/atrium/internal/application"
	"github.com/jeiang/atrium/internal/notify"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
	"github.com/jeiang/atrium/internal/web"
)

type InboxQuery struct {
	Cursor api.Cursor `query:"cursor" json:"-"`
	Limit  api.Limit  `query:"limit" json:"-"`
}

type InboxPage struct {
	Items      []notify.Notification `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

type inboxKey struct {
	ID int64 `json:"id"`
}

type InboxReadState struct {
	ID   int64 `path:"id" json:"-"`
	Read bool  `json:"read"`
}

type InboxItem struct {
	ID int64 `path:"id" json:"-"`
}

type ResendResult struct {
	ID        int64  `json:"id"`
	Delivered bool   `json:"delivered"`
	Error     string `json:"error"`
	Outcome   string `json:"outcome"`
}

type DeviceRegistration struct {
	Transport string `json:"transport,omitempty"`
	Endpoint  string `json:"endpoint"`
	P256DH    string `json:"p256dh"`
	Auth      string `json:"auth"`
	Label     string `json:"label,omitempty"`
}

type RegisteredDevice struct {
	ID int64 `json:"id"`
}

type DeviceItem struct {
	ID int64 `path:"device_id" json:"-"`
}

type DeviceTestResult struct {
	Sent    int    `json:"sent"`
	Failure string `json:"failure"`
	Outcome string `json:"outcome"`
}

type NotificationSettings struct {
	Channels []notify.Channel            `json:"channels"`
	Error    string                      `json:"error"`
	Push     notifyapp.PushConfiguration `json:"push"`
	Phone    PhoneSettings               `json:"phone"`
}

type PhoneSettings struct {
	Enabled     bool                `json:"enabled"`
	Kinds       []notifyapp.KindRow `json:"kinds"`
	Outstanding int                 `json:"outstanding"`
}

type ChannelContent struct {
	Channel string `path:"channel" json:"-"`
	Content string `json:"content"`
}

type PhonePolicy struct {
	Enabled bool     `json:"enabled"`
	Kinds   []string `json:"kinds"`
}

func notificationRoutes(d *web.Deps, svc *notifyapp.Service) []application.Binding {
	defs := map[application.Name]application.Definition{}
	for _, def := range notifyapp.Describe() {
		defs[def.Name] = def
	}
	shape := func(area application.Area, response any, success int, errors ...application.Code) application.APIShape {
		s := application.APIShape{Area: area, Success: success, Errors: errors}
		if response != nil {
			s.Response = reflect.TypeOf(response)
		}
		return s
	}
	bodiless := func(s application.APIShape) application.APIShape {
		s.Body = application.BodyNone
		return s
	}
	return []application.Binding{
		api.Query(d, defs[notifyapp.InboxName], shape("inbox", InboxPage{}, 0), "GET /inbox",
			func(ctx context.Context, in InboxQuery) (application.Read[InboxPage], error) {
				var last inboxKey
				if in.Cursor.Set() {
					if err := in.Cursor.Last(&last); err != nil {
						return application.Read[InboxPage]{}, err
					}
				}
				read, err := svc.Inbox(ctx, notifyapp.InboxInput{Before: last.ID, Limit: int(in.Limit)})
				if err != nil {
					return application.Read[InboxPage]{}, err
				}
				page := InboxPage{Items: read.Data.Items}
				if read.Data.More {
					page.NextCursor = in.Cursor.Next(inboxKey{ID: page.Items[len(page.Items)-1].ID})
				}
				return application.Read[InboxPage]{Data: page, Revision: read.Revision}, nil
			}),
		api.Command(d, defs[notifyapp.MarkName], shape("inbox", nil, http.StatusNoContent, application.NotFound), "PUT /inbox/{id}/read",
			func(ctx context.Context, in InboxReadState) (application.Change[notifyapp.Empty], error) {
				return svc.Mark(ctx, notifyapp.MarkInput{ID: in.ID, Read: in.Read})
			}),
		api.Command(d, defs[notifyapp.ResendName], bodiless(shape("inbox", ResendResult{}, 0, application.Refused, application.NotFound)), "POST /inbox/{id}/resend",
			func(ctx context.Context, in InboxItem) (application.Change[ResendResult], error) {
				change, err := svc.Resend(ctx, notifyapp.ResendInput{ID: in.ID})
				sent := change.Data.Sent
				return application.Change[ResendResult]{Data: ResendResult{ID: sent.ID, Delivered: sent.Delivered, Error: sent.Error, Outcome: change.Data.Outcome}, AuditID: change.AuditID}, err
			}),
		api.Query(d, defs[notifyapp.ConfigurationName], shape("devices", NotificationSettings{}, 0), "GET /notifications",
			func(ctx context.Context, in notifyapp.Empty) (application.Read[NotificationSettings], error) {
				read, err := svc.Configuration(ctx, in)
				if err != nil {
					return application.Read[NotificationSettings]{}, err
				}
				c := read.Data
				return application.Read[NotificationSettings]{Data: NotificationSettings{
					Channels: c.Channels, Error: c.Error, Push: c.Push,
					Phone: PhoneSettings{Enabled: c.Phone.Enabled, Kinds: c.Phone.Kinds, Outstanding: c.Phone.Outstanding},
				}, Revision: read.Revision}, nil
			}),
		api.Command(d, defs[notifyapp.ChannelContentName], shape("devices", nil, http.StatusNoContent, application.Refused), "PUT /notifications/channels/{channel}",
			func(ctx context.Context, in ChannelContent) (application.Change[notifyapp.Empty], error) {
				return svc.ChannelContent(ctx, notifyapp.ChannelContentInput{Name: in.Channel, Content: in.Content})
			}),
		api.Command(d, defs[notifyapp.SubscribeName], shape("devices", RegisteredDevice{}, http.StatusCreated, application.Invalid, application.Refused), "POST /devices",
			func(ctx context.Context, in DeviceRegistration) (application.Change[RegisteredDevice], error) {
				change, err := svc.Subscribe(ctx, notifyapp.SubscribeInput{Transport: in.Transport, Endpoint: in.Endpoint, P256DH: in.P256DH, Auth: in.Auth, Label: in.Label})
				return application.Change[RegisteredDevice]{Data: RegisteredDevice{ID: change.Data.ID}, AuditID: change.AuditID}, err
			}),
		api.Command(d, defs[notifyapp.UnsubscribeName], bodiless(shape("devices", nil, http.StatusNoContent)), "DELETE /devices/{device_id}",
			func(ctx context.Context, in DeviceItem) (application.Change[notifyapp.Empty], error) {
				return svc.Unsubscribe(ctx, notifyapp.UnsubscribeInput{DeviceID: &in.ID})
			}),
		api.Command(d, defs[notifyapp.TestName], shape("devices", DeviceTestResult{}, 0, application.Refused), "POST /devices/test",
			func(ctx context.Context, in notifyapp.Empty) (application.Change[DeviceTestResult], error) {
				change, err := svc.Test(ctx, in)
				t := change.Data
				return application.Change[DeviceTestResult]{Data: DeviceTestResult{Sent: t.Sent, Failure: t.Failure, Outcome: t.Outcome}, AuditID: change.AuditID}, err
			}),
		api.Command(d, defs[notifyapp.PhonePolicyName], shape("permissions", nil, http.StatusNoContent, application.Invalid), "PUT /permissions/phone",
			func(ctx context.Context, in PhonePolicy) (application.Change[notifyapp.Empty], error) {
				var fields []application.Field
				for _, kind := range in.Kinds {
					if policy.WebOnly[kind] {
						fields = append(fields, application.Field{Name: "kinds", Code: "web_only", Message: kind + " can only be decided on the web."})
					}
				}
				if fields != nil {
					return application.Change[notifyapp.Empty]{}, &application.Error{Code: application.Invalid, Message: "Some kinds cannot be decided from a phone.", Fields: fields}
				}
				return svc.PhonePolicy(ctx, notifyapp.PhonePolicyInput{Enabled: in.Enabled, Kinds: in.Kinds})
			}),
		api.Command(d, defs[notifyapp.RevokeLinksName], shape("permissions", nil, http.StatusNoContent), "POST /permissions/phone/revoke-links",
			func(ctx context.Context, in notifyapp.Empty) (application.Change[notifyapp.Empty], error) {
				return svc.RevokeLinks(ctx, in)
			}),
	}
}
