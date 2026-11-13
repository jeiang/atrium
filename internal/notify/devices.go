package notify

import (
	"context"
	"crypto/ecdh"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
)

type PushDevice struct {
	ID        int64    `json:"id"`
	Transport string   `json:"transport"`
	Endpoint  string   `json:"endpoint"`
	Label     string   `json:"label"`
	CreatedTS float64  `json:"created_ts"`
	LastOKTS  *float64 `json:"last_ok_ts"`
	Failures  int      `json:"failure_count"`
	LastError *string  `json:"last_error"`
}

type RegisterDeviceInput struct {
	Transport string
	Endpoint  string
	P256DH    string
	Auth      string
	Label     string
}

type webPushCredentials struct {
	endpoint, key, secret string
}

func checkWebPush(endpoint, p256dh, auth string) (webPushCredentials, error) {
	endpoint = strings.TrimSpace(endpoint)
	if err := checkEndpoint(endpoint); err != nil {
		return webPushCredentials{}, err
	}
	public, err := pushKey(p256dh)
	if err != nil {
		return webPushCredentials{}, PushError("p256dh must be base64url")
	}
	if _, err := ecdh.P256().NewPublicKey(public); err != nil {
		return webPushCredentials{}, PushError("p256dh is not a P-256 public key")
	}
	secret, err := pushKey(auth)
	if err != nil || len(secret) != 16 {
		return webPushCredentials{}, PushError("auth must be 16 bytes of base64url")
	}
	return webPushCredentials{endpoint: endpoint, key: base64.RawURLEncoding.EncodeToString(public), secret: base64.RawURLEncoding.EncodeToString(secret)}, nil
}

func RegisterDevice(ctx context.Context, q db.Querier, in RegisterDeviceInput) (int64, error) {
	if err := checkTransport(in.Transport); err != nil {
		return 0, err
	}
	creds, err := checkWebPush(in.Endpoint, in.P256DH, in.Auth)
	if err != nil {
		return 0, err
	}
	if err := ledger.CheckAdmission(ctx); err != nil {
		return 0, err
	}
	label := Plain(in.Label, pushLabelChars)
	var id int64
	err = q.QueryRowContext(ctx, "SELECT device_id FROM webpush_devices WHERE endpoint = ?", creds.endpoint).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := q.ExecContext(ctx, "INSERT INTO notification_devices (transport, label, created_ts) VALUES (?, ?, ?)", in.Transport, label, stamp())
		if err != nil {
			return 0, err
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, err
		}
		_, err = q.ExecContext(ctx, "INSERT INTO webpush_devices (device_id, endpoint, p256dh, auth) VALUES (?, ?, ?, ?)", id, creds.endpoint, creds.key, creds.secret)
		return id, err
	case err != nil:
		return 0, err
	}
	if _, err := q.ExecContext(ctx, "UPDATE webpush_devices SET p256dh = ?, auth = ? WHERE device_id = ?", creds.key, creds.secret, id); err != nil {
		return 0, err
	}
	_, err = q.ExecContext(ctx, "UPDATE notification_devices SET label = ?, failure_count = 0, last_error = NULL WHERE id = ?", label, id)
	return id, err
}

func Unsubscribe(ctx context.Context, q db.Querier, endpoint string) error {
	if err := ledger.CheckAdmission(ctx); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM notification_devices WHERE id IN (SELECT device_id FROM webpush_devices WHERE endpoint = ?)", strings.TrimSpace(endpoint))
	return err
}

func RemoveDevice(ctx context.Context, q db.Querier, id int64) error {
	if err := ledger.CheckAdmission(ctx); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, "DELETE FROM notification_devices WHERE id = ?", id)
	return err
}

func Devices(ctx context.Context, q db.Querier) ([]PushDevice, error) {
	rows, err := q.QueryContext(ctx, `SELECT d.id, d.transport, COALESCE(w.endpoint, ''), d.label, d.created_ts, d.last_ok_ts, d.failure_count, d.last_error
		FROM notification_devices d LEFT JOIN webpush_devices w ON w.device_id = d.id ORDER BY d.id`)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (PushDevice, error) {
		var d PushDevice
		return d, r.Scan(&d.ID, &d.Transport, &d.Endpoint, &d.Label, &d.CreatedTS, &d.LastOKTS, &d.Failures, &d.LastError)
	})
}

func deviceCount(ctx context.Context, q db.Querier) (int, error) {
	var n int
	err := q.QueryRowContext(ctx, "SELECT COUNT(*) FROM notification_devices").Scan(&n)
	return n, err
}

func pushTargets(ctx context.Context, q db.Querier) ([]Device, error) {
	rows, err := q.QueryContext(ctx, "SELECT id, transport FROM notification_devices ORDER BY id")
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Device, error) {
		var d Device
		return d, r.Scan(&d.ID, &d.Transport)
	})
}

func pushAll(ctx context.Context, d *db.DB, wake Wakeup) (sent int, failure string, err error) {
	targets, err := pushTargets(ctx, d.Read)
	if err != nil || len(targets) == 0 {
		return 0, "", err
	}
	open := map[string]PushTransport{}
	var failures []string
	for _, device := range targets {
		t, ok := open[device.Transport]
		if !ok {
			opener, known := transports[device.Transport]
			if !known {
				failures = append(failures, UnsupportedTransport(device.Transport).Error())
				open[device.Transport] = nil
				continue
			}
			var closer func()
			var problem string
			t, closer, problem, err = opener(ctx, d)
			if err != nil {
				return sent, "", err
			}
			if problem != "" {
				failures = append(failures, problem)
			} else {
				defer closer()
			}
			open[device.Transport] = t
		}
		if t == nil {
			continue
		}
		out, err := t.Send(ctx, device, wake)
		if err != nil {
			return sent, "", err
		}
		if err := recordPush(ctx, d.Write, device.ID, out); err != nil {
			return sent, "", err
		}
		if out.Delivered {
			sent++
		} else {
			failures = append(failures, out.ErrorClass)
		}
	}
	if sent > 0 {
		return sent, "", nil
	}
	return 0, strings.Join(failures, "; "), nil
}

func recordPush(ctx context.Context, q db.Querier, id int64, out Delivery) error {
	var err error
	switch {
	case out.Gone:
		_, err = q.ExecContext(ctx, "DELETE FROM notification_devices WHERE id = ?", id)
	case out.Delivered:
		_, err = q.ExecContext(ctx, "UPDATE notification_devices SET failure_count = 0, last_error = NULL, last_ok_ts = ? WHERE id = ?", stamp(), id)
	default:
		_, err = q.ExecContext(ctx, "UPDATE notification_devices SET failure_count = failure_count + 1, last_error = ? WHERE id = ?", out.ErrorClass, id)
	}
	return err
}

func deliverPush(ctx context.Context, d *db.DB, id int64, m delivery, link string) (tried, delivered bool, failure string, err error) {
	if m.channel == "" {
		m.channel = Assistant
	}
	if n, err := deviceCount(ctx, d.Read); err != nil || n == 0 {
		return false, false, "", err
	}
	c, err := GetChannel(ctx, d.Read, m.channel)
	if err != nil {
		return true, false, "", err
	}
	sent, failure, err := pushAll(ctx, d, Wakeup{
		NotificationID: id, Channel: m.channel, FullText: c.FullText(), Title: m.title, Body: m.body,
		URL: pushURL(link, m.click), Priority: m.priority, Actions: m.actions,
	})
	return true, sent > 0, failure, err
}

func testWakeup() Wakeup {
	return Wakeup{Channel: System, FullText: true, Title: "atrium test", Body: "Test push notification.", URL: pushInbox}
}

func PushTest(ctx context.Context, d *db.DB) (int, string, error) {
	if err := ledger.CheckAdmission(ctx); err != nil {
		return 0, "", err
	}
	return pushAll(ctx, d, testWakeup())
}

func ImportDevices(ctx context.Context, src db.Querier, dst db.Querier) (int64, []string, error) {
	rows, err := src.QueryContext(ctx, "SELECT id, endpoint, p256dh, auth, label, created_ts, last_ok_ts, failure_count, last_error FROM push_subscriptions ORDER BY id")
	if err != nil {
		return 0, nil, err
	}
	type legacy struct {
		id                     int64
		endpoint, p256dh, auth string
		label                  string
		created                float64
		lastOK                 *float64
		failures               int64
		lastError              *string
	}
	sources, err := db.Collect(rows, func(r *sql.Rows) (legacy, error) {
		var l legacy
		return l, r.Scan(&l.id, &l.endpoint, &l.p256dh, &l.auth, &l.label, &l.created, &l.lastOK, &l.failures, &l.lastError)
	})
	if err != nil {
		return 0, nil, err
	}
	var carried int64
	var notes []string
	for _, l := range sources {
		creds, err := checkWebPush(l.endpoint, l.p256dh, l.auth)
		if err != nil {
			notes = append(notes, fmt.Sprintf("push_subscriptions row %d rejected: %s", l.id, err))
			continue
		}
		if _, err := dst.ExecContext(ctx, "INSERT INTO notification_devices (id, transport, label, created_ts, last_ok_ts, failure_count, last_error) VALUES (?, ?, ?, ?, ?, ?, ?)",
			l.id, TransportWebPush, Plain(l.label, pushLabelChars), l.created, l.lastOK, l.failures, l.lastError); err != nil {
			return carried, notes, err
		}
		if _, err := dst.ExecContext(ctx, "INSERT INTO webpush_devices (device_id, endpoint, p256dh, auth) VALUES (?, ?, ?, ?)", l.id, creds.endpoint, creds.key, creds.secret); err != nil {
			return carried, notes, err
		}
		carried++
	}
	return carried, notes, nil
}
