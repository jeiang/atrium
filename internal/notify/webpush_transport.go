package notify

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	"go.opentelemetry.io/otel/trace"

	"github.com/jeiang/atrium/internal/connections/transport"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/obs"
	"github.com/jeiang/atrium/internal/tool"
)

type webPushTarget struct {
	endpoint, key, secret string
}

type webPush struct {
	options *webpush.Options
	targets map[int64]webPushTarget
}

func openWebPush(ctx context.Context, d *db.DB) (PushTransport, func(), string, error) {
	rows, err := d.Read.QueryContext(ctx, "SELECT device_id, endpoint, p256dh, auth FROM webpush_devices")
	if err != nil {
		return nil, nil, "", err
	}
	targets := map[int64]webPushTarget{}
	if _, err := db.Collect(rows, func(r *sql.Rows) (struct{}, error) {
		var id int64
		var t webPushTarget
		if err := r.Scan(&id, &t.endpoint, &t.key, &t.secret); err != nil {
			return struct{}{}, err
		}
		targets[id] = t
		return struct{}{}, nil
	}); err != nil {
		return nil, nil, "", err
	}
	lease, err := pushConnection(ctx, d)
	if err != nil {
		return nil, nil, "", err
	}
	subject := strings.TrimSpace(connectionConfig(lease)["subject"])
	if subject == "" {
		lease.Close()
		return nil, nil, "web push not configured (no subject)", nil
	}
	keys, err := vapidKeys(lease)
	if err != nil {
		lease.Close()
		return nil, nil, "", err
	}
	client, err := pushClient(lease)
	if err != nil {
		lease.Close()
		var te transport.Error
		if errors.As(err, &te) {
			return nil, nil, "web push unreachable: " + te.Error(), nil
		}
		return nil, nil, "", err
	}
	closer := func() {
		client.CloseIdleConnections()
		lease.Close()
	}
	options := &webpush.Options{
		HTTPClient: client, Subscriber: strings.TrimPrefix(subject, "mailto:"), TTL: pushTTL,
		VAPIDPublicKey: keys.public, VAPIDPrivateKey: keys.private,
	}
	return &webPush{options: options, targets: targets}, closer, "", nil
}

func (*webPush) Kind() string { return TransportWebPush }

func (w *webPush) Send(ctx context.Context, device Device, wake Wakeup) (out Delivery, err error) {
	ctx, span := obs.Start(ctx, "notify.deliver", trace.WithAttributes(
		obs.String(obs.NotifyChannel, wake.Channel),
		obs.String(obs.NotifyBackend, TransportWebPush),
	))
	attempted := false
	defer func() {
		span.SetAttributes(obs.Bool(obs.NotifyDelivered, out.Delivered))
		if err != nil {
			obs.Fail(span, "error")
		} else if !out.Delivered {
			span.SetAttributes(obs.String(obs.ErrorType, "delivery_failed"))
		}
		span.End()
		if attempted {
			obs.NotifyDelivery.Add(1, wake.Channel, TransportWebPush, strconv.FormatBool(out.Delivered))
		}
	}()
	target, ok := w.targets[device.ID]
	if !ok {
		return Delivery{ErrorClass: "web push device has no subscription"}, nil
	}
	if problem := checkEndpoint(target.endpoint); problem != nil {
		return Delivery{ErrorClass: problem.Error()}, nil
	}
	title, body := wake.Title, tool.Clip(wake.Body, pushBodyChars)
	if !wake.FullText {
		title, body = privateTitle, privateBody
	}
	payload, err := json.Marshal(pushPayload{Title: title, Body: body, URL: wake.URL, Actions: pushActions(wake.Actions)})
	if err != nil {
		return Delivery{}, err
	}
	options := *w.options
	options.Urgency = urgency(wake.Priority)
	attempted = true
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: target.endpoint, Keys: webpush.Keys{P256dh: target.key, Auth: target.secret},
	}, &options)
	if err == nil {
		_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
	}
	if err != nil {
		if ctx.Err() != nil {
			return Delivery{}, ctx.Err()
		}
		failure := "web push unreachable: " + pushErrorClass(err)
		pushLog.Warn("web push delivery failed", logx.Err, failure)
		return Delivery{ErrorClass: failure}, nil
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		pushLog.Info("web push subscription expired; removed", logx.Reason, strconv.Itoa(resp.StatusCode))
		return Delivery{Gone: true, ErrorClass: fmt.Sprintf("web push %d: subscription removed", resp.StatusCode)}, nil
	case resp.StatusCode >= 300:
		failure := fmt.Sprintf("web push %d", resp.StatusCode)
		pushLog.Warn("web push delivery failed", logx.Err, failure)
		return Delivery{ErrorClass: failure}, nil
	}
	return Delivery{Delivered: true}, nil
}
