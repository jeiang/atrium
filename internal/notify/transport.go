package notify

import (
	"context"
	"slices"

	"github.com/jeiang/atrium/internal/db"
)

const TransportWebPush = "webpush"

type Device struct {
	ID        int64
	Transport string
}

type Wakeup struct {
	NotificationID int64
	Channel        string
	FullText       bool
	Title          string
	Body           string
	URL            string
	Priority       int
	Actions        []Action
}

type Delivery struct {
	Delivered  bool
	Gone       bool
	ErrorClass string
}

type PushTransport interface {
	Kind() string
	Send(context.Context, Device, Wakeup) (Delivery, error)
}

type transportOpener func(context.Context, *db.DB) (transport PushTransport, closer func(), failure string, err error)

var transports = map[string]transportOpener{TransportWebPush: openWebPush}

type UnsupportedTransport string

func (e UnsupportedTransport) Error() string { return "unsupported push transport " + string(e) }

func Transports() []string {
	kinds := make([]string, 0, len(transports))
	for kind := range transports {
		kinds = append(kinds, kind)
	}
	slices.Sort(kinds)
	return kinds
}

func checkTransport(kind string) error {
	if _, ok := transports[kind]; !ok {
		return UnsupportedTransport(kind)
	}
	return nil
}
