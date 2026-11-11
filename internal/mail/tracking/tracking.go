package tracking

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/ledger"
	"github.com/jeiang/atrium/internal/logx"
	"github.com/jeiang/atrium/internal/notify"
	"github.com/jeiang/atrium/internal/settings"
)

var log = logx.Logger("atrium.tracking")

const Module = "mail"

const (
	KeyMonthlyQuota        = "mail_ship24_monthly_quota"
	KeyDestinationCountry  = "mail_ship24_destination_country"
	KeyDestinationPostcode = "mail_ship24_destination_postcode"
	DefaultURL             = "https://api.ship24.com/public/v1"
	delivered              = "delivered"
	exception              = "exception"
	outForDelivery         = "out_for_delivery"
	inTransit              = "in_transit"
	shipped                = "shipped"
	ordered                = "ordered"
	UndoUntrack            = "package_untrack"
)

func init() {
	adv := func(key string, kind settings.Type, def string, check func(string) error) settings.Def {
		return settings.Value(settings.Advanced, settings.Def{Key: key, Module: Module, Type: kind, Default: def, Check: check})
	}
	settings.MustRegister(
		adv(KeyMonthlyQuota, settings.Int, "10", settings.Between(1, 1000)),
		settings.Shown("Destination country", adv(KeyDestinationCountry, settings.String, "", countryCheck)),
		settings.Shown("Destination postcode", adv(KeyDestinationPostcode, settings.String, "", nil)),
	)
}

func countryCheck(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if len(value) < 2 || len(value) > 3 {
		return settings.Invalid("must be a two- or three-letter country code")
	}
	for _, c := range value {
		if c < 'A' || c > 'Z' {
			return settings.Invalid("must be a two- or three-letter country code")
		}
	}
	return nil
}

type Error string

func (e Error) Error() string { return string(e) }

type QuotaError string

func (e QuotaError) Error() string { return string(e) }

var (
	ErrTrackerGone = errors.New("the tracker is gone")
	ErrBadReply    = errors.New("Ship24 returned an unexpected reply")
)

type StatusError struct {
	Status  int
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("Ship24 answered %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("Ship24 answered %d", e.Status)
}

type RateLimitError struct{ Status int }

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("Ship24 rate limited the request (%d)", e.Status)
}

type TransportError struct{ Err error }

func (e *TransportError) Error() string { return "Ship24 is unreachable" }
func (e *TransportError) Unwrap() error { return e.Err }

func OutcomeUnknown(err error) bool {
	var transport *TransportError
	if errors.As(err, &transport) {
		return true
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, ErrBadReply) {
		return true
	}
	var status *StatusError
	if errors.As(err, &status) {
		return status.Status >= 500
	}
	return false
}

type Settings struct {
	URL, Key            string
	Quota, Timeout      int
	PollLimit, PollDays int
	Country, Postcode   string
	lease               *connections.Lease
}

func (s Settings) Close() {
	if s.lease != nil {
		s.lease.Close()
	}
}

func Load(ctx context.Context, q db.Querier) (Settings, error) {
	read := func(key string) (string, error) {
		found, err := settings.From(ctx).Resolve(ctx, q, key)
		return strings.TrimSpace(found.Value), err
	}
	var s Settings
	var err error
	s.Timeout, s.PollLimit, s.PollDays = TuningMailShip24TimeoutSeconds, TuningMailShip24PollLimit, TuningMailShip24PollDays
	var lease *connections.Lease
	if store := connections.DefaultFor(q); store != nil {
		lease, err = store.Open(ctx, ConnectionID)
	} else {
		lease, err = connections.OpenRead(ctx, q, config.From(ctx).Host(), ConnectionID)
	}
	if err != nil {
		var unknown connections.UnknownID
		if errors.As(err, &unknown) {
			return Settings{}, config.NotConfigured("the Ship24 connection is not configured")
		}
		return Settings{}, err
	}
	s.lease = lease
	if s.URL, err = lease.Endpoint("url"); err != nil {
		lease.Close()
		return Settings{}, config.NotConfigured("the Ship24 connection has no URL")
	}
	if s.Key, err = lease.Credential("api_key"); err != nil {
		var missing connections.MissingCredential
		if !errors.As(err, &missing) {
			lease.Close()
			return Settings{}, err
		}
		s.Key = ""
	}
	quota, err := read(KeyMonthlyQuota)
	if err != nil {
		lease.Close()
		return Settings{}, err
	}
	if s.Quota, err = strconv.Atoi(quota); err != nil {
		lease.Close()
		return Settings{}, err
	}
	if s.Country, err = read(KeyDestinationCountry); err != nil {
		lease.Close()
		return Settings{}, err
	}
	if s.Postcode, err = read(KeyDestinationPostcode); err != nil {
		lease.Close()
		return Settings{}, err
	}
	return s, nil
}

type Client struct {
	base, key string
	http      *http.Client
}

func NewClient(client *http.Client, base, key string) *Client {
	return &Client{http: client, base: strings.TrimRight(base, "/"), key: key}
}

func (c *Client) Close() { c.http.CloseIdleConnections() }

type Tracker struct {
	TrackerID      string `json:"trackerId"`
	TrackingNumber string `json:"trackingNumber"`
}

type Event struct {
	EventID            string `json:"eventId"`
	Status             string `json:"status"`
	Description        string `json:"description"`
	OccurrenceDatetime string `json:"occurrenceDatetime"`
	Location           string `json:"location"`
	StatusCode         string `json:"statusCode"`
	StatusMilestone    string `json:"statusMilestone"`
}

type Tracking struct {
	Tracker  Tracker `json:"tracker"`
	Shipment struct {
		StatusCode      string `json:"statusCode"`
		StatusMilestone string `json:"statusMilestone"`
	} `json:"shipment"`
	Events []Event `json:"events"`
}

func (c *Client) Create(ctx context.Context, number, courier, country, postcode string) (Tracker, error) {
	body := map[string]any{"trackingNumber": number}
	if courier != "" {
		body["courierCode"] = []string{courier}
	}
	if country != "" {
		body["destinationCountryCode"] = country
	}
	if postcode != "" {
		body["destinationPostCode"] = postcode
	}
	status, raw, err := c.do(ctx, http.MethodPost, "/trackers", body)
	if err != nil {
		return Tracker{}, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Tracker{}, Error("Ship24 refused the API key")
	case status == http.StatusTooManyRequests:
		return Tracker{}, &RateLimitError{Status: status}
	case status != http.StatusCreated && status != http.StatusOK:
		return Tracker{}, &StatusError{Status: status, Message: clip(string(raw), 200)}
	}
	var reply struct {
		Data struct {
			Tracker Tracker `json:"tracker"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Data.Tracker.TrackerID == "" {
		return Tracker{}, fmt.Errorf("%w: no tracker id", ErrBadReply)
	}
	return reply.Data.Tracker, nil
}

func (c *Client) Results(ctx context.Context, trackerID string) (Tracking, error) {
	status, raw, err := c.do(ctx, http.MethodGet, "/trackers/"+url.PathEscape(trackerID)+"/results", nil)
	if err != nil {
		return Tracking{}, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Tracking{}, Error("Ship24 refused the API key")
	case status == http.StatusNotFound:
		return Tracking{}, fmt.Errorf("%w: %s", ErrTrackerGone, trackerID)
	case status == http.StatusTooManyRequests:
		return Tracking{}, &RateLimitError{Status: status}
	case status != http.StatusOK:
		return Tracking{}, &StatusError{Status: status}
	}
	var reply struct {
		Data struct {
			Trackings []Tracking `json:"trackings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || len(reply.Data.Trackings) == 0 {
		return Tracking{}, fmt.Errorf("%w: no tracking results", ErrBadReply)
	}
	return reply.Data.Trackings[0], nil
}

func (c *Client) Unsubscribe(ctx context.Context, trackerID string) error {
	status, _, err := c.do(ctx, http.MethodPatch, "/trackers/"+url.PathEscape(trackerID), map[string]any{"isSubscribed": false})
	if err != nil {
		return err
	}
	switch {
	case status == http.StatusNotFound:
		return nil
	case status == http.StatusTooManyRequests:
		return &RateLimitError{Status: status}
	case status != http.StatusOK:
		return &StatusError{Status: status}
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, nil, Error("the Ship24 URL is invalid")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, nil, ctx.Err()
		}
		return 0, nil, &TransportError{Err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, Error("Ship24 did not return a complete reply")
	}
	return resp.StatusCode, raw, nil
}

func clip(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= limit {
		return text
	}
	return text[:limit]
}

type Target struct {
	ID                  int64
	TrackingNumber      string
	Carrier             string
	Status              string
	StatusText          string
	Aggregator          string
	TrackerID           string
	LastEvent           string
	LastEventHash       string
	NotifiedStatus      string
	LastEventTS         float64
	LastSyncTS          float64
	TrackerRegisteredTS float64
}

type Result struct {
	PackageID  int64     `json:"package_id"`
	TrackerID  string    `json:"tracker_id"`
	Status     string    `json:"status"`
	StatusText string    `json:"status_text"`
	Events     []Event   `json:"events"`
	WriteID    int64     `json:"write_id,omitempty"`
	Tracking   *Tracking `json:"-"`
}

func Identity(carrier, number string) string {
	return "tracking:" + courierCode(carrier) + ":" + strings.ToUpper(strings.TrimSpace(number))
}

func Resolve(ctx context.Context, d *db.DB, env config.Env, now float64, packageID int64, number, carrier string) (Target, Settings, error) {
	s, err := Load(ctx, d.Read)
	if err != nil {
		return Target{}, Settings{}, err
	}
	var t Target
	switch {
	case packageID > 0:
		err = d.Read.QueryRowContext(ctx,
			"SELECT id, tracking_number, carrier, status, status_text, aggregator, tracker_id, last_event, last_event_hash, "+
				"notified_status, last_event_ts, last_sync_ts, tracker_registered_ts FROM packages WHERE id = ?", packageID).
			Scan(&t.ID, &t.TrackingNumber, &t.Carrier, &t.Status, &t.StatusText, &t.Aggregator, &t.TrackerID, &t.LastEvent,
				&t.LastEventHash, &t.NotifiedStatus, &t.LastEventTS, &t.LastSyncTS, &t.TrackerRegisteredTS)
		if errors.Is(err, sql.ErrNoRows) {
			return Target{}, Settings{}, Error("no package with id " + strconv.FormatInt(packageID, 10))
		}
	case number != "":
		err = d.Read.QueryRowContext(ctx,
			"SELECT id, tracking_number, carrier, status, status_text, aggregator, tracker_id, last_event, last_event_hash, "+
				"notified_status, last_event_ts, last_sync_ts, tracker_registered_ts FROM packages WHERE tracking_number = ?", number).
			Scan(&t.ID, &t.TrackingNumber, &t.Carrier, &t.Status, &t.StatusText, &t.Aggregator, &t.TrackerID, &t.LastEvent,
				&t.LastEventHash, &t.NotifiedStatus, &t.LastEventTS, &t.LastSyncTS, &t.TrackerRegisteredTS)
		if errors.Is(err, sql.ErrNoRows) {
			t = Target{TrackingNumber: number, Carrier: carrier, Status: ordered}
			err = nil
		}
	default:
		return Target{}, Settings{}, Error("give package_id or tracking_number")
	}
	if err != nil {
		return Target{}, Settings{}, err
	}
	if t.TrackerID != "" {
		return t, s, nil
	}
	monthStart, err := monthStart(ctx, d.Read, env, now)
	if err != nil {
		return Target{}, Settings{}, err
	}
	var blocked int
	blocked, err = ledger.BlockedCount(ctx, d.Read, "track_package", Identity(t.Carrier, t.TrackingNumber))
	if err != nil {
		return Target{}, Settings{}, err
	}
	if blocked > 0 {
		return Target{}, Settings{}, Error("the previous track_package for this package has an unknown outcome; resolve it with `atrium effects list --blocked` before retrying")
	}
	var used int
	used, err = ledger.CountSince(ctx, d.Read, "track_package", monthStart)
	if err != nil {
		return Target{}, Settings{}, err
	}
	if used >= s.Quota {
		return Target{}, Settings{}, QuotaError(fmt.Sprintf(
			"the Ship24 monthly quota is exhausted (%d of %d registrations used this month; raise %s or wait until next month)",
			used, s.Quota, KeyMonthlyQuota))
	}
	return t, s, nil
}

func Admit(ctx context.Context, tx *sql.Tx, t Target, now float64) (int64, error) {
	if t.ID != 0 {
		return t.ID, nil
	}
	res, err := tx.ExecContext(ctx,
		"INSERT INTO packages (carrier, tracking_number, merchant, merchant_key, order_ref, status, status_text, "+
			"source_message_id, last_message_id, updated_ts, first_seen) VALUES (?, ?, '', '', '', ?, '', 0, 0, ?, ?)",
		t.Carrier, t.TrackingNumber, ordered, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func monthStart(ctx context.Context, q db.Querier, env config.Env, now float64) (float64, error) {
	loc, err := config.From(ctx).Location(env)
	if err != nil {
		return 0, err
	}
	at := time.Unix(int64(now), 0).In(loc)
	return float64(time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, loc).Unix()), nil
}

func clientFor(ctx context.Context, q db.Querier, s Settings) (*Client, error) {
	if s.Key == "" {
		return nil, config.NotConfigured("the Ship24 connection has no API key")
	}
	if s.lease == nil {
		return nil, config.NotConfigured("the Ship24 connection is not configured")
	}
	hc, err := s.lease.Client("url", time.Duration(s.Timeout)*time.Second)
	if err != nil {
		return nil, err
	}
	return NewClient(hc, s.URL, s.Key), nil
}

func Register(ctx context.Context, d *db.DB, now float64, t Target, s Settings) (Result, error) {
	client, err := clientFor(ctx, d.Read, s)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	tracker, err := client.Create(ctx, t.TrackingNumber, courierCode(t.Carrier), s.Country, s.Postcode)
	if err != nil {
		return Result{}, err
	}
	out := Result{PackageID: t.ID, TrackerID: tracker.TrackerID, Status: t.Status, StatusText: t.StatusText}
	tracking, terr := client.Results(ctx, tracker.TrackerID)
	if terr != nil {
		return out, nil
	}
	out.Status = advance(t.Status, statusOf(tracking))
	out.StatusText = textOf(tracking)
	out.Events = tracking.Events
	out.Tracking = &tracking
	return out, nil
}

func SaveRegistration(ctx context.Context, tx *sql.Tx, now float64, id int64, trackerID string, tracking *Tracking) error {
	if tracking != nil {
		for _, e := range tracking.Events {
			if _, err := tx.ExecContext(ctx,
				"INSERT OR IGNORE INTO package_events (package_id, tracker_id, event_key, event_ts, status, status_code, status_milestone, description, location, created_ts) "+
					"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				id, trackerID, eventKey(e), eventTime(e, now), e.Status, e.StatusCode, e.StatusMilestone, descriptionOf(e), e.Location, now); err != nil {
				return err
			}
		}
	}
	sets := []string{"aggregator = 'ship24'", "tracker_id = ?", "last_sync_ts = ?",
		"tracker_registered_ts = CASE WHEN tracker_registered_ts = 0 THEN ? ELSE tracker_registered_ts END"}
	args := []any{trackerID, now, now}
	if tracking != nil && (statusOf(*tracking) != "" || len(tracking.Events) > 0) {
		latest := latestEvent(*tracking)
		sets = append(sets, "status = ?", "status_text = ?", "last_event = ?", "last_event_ts = ?", "last_event_hash = ?")
		args = append(args, statusOf(*tracking), textOf(*tracking), textOf(*tracking), eventTime(latest, now), eventKey(latest))
	}
	_, err := tx.ExecContext(ctx, "UPDATE packages SET "+strings.Join(sets, ", ")+" WHERE id = ?", append(args, id)...)
	return err
}

func Refresh(ctx context.Context, d *db.DB, now float64, t Target, s Settings, env config.Env) (Result, bool, error) {
	client, err := clientFor(ctx, d.Read, s)
	if err != nil {
		return Result{}, false, err
	}
	defer client.Close()
	tracking, err := client.Results(ctx, t.TrackerID)
	if err != nil {
		return Result{}, false, err
	}
	notified, err := save(ctx, d, now, t.ID, t.TrackerID, tracking, true, env)
	if err != nil {
		return Result{}, false, err
	}
	return Result{PackageID: t.ID, TrackerID: t.TrackerID, Status: advance(t.Status, statusOf(tracking)), StatusText: textOf(tracking), Events: tracking.Events}, notified, nil
}

type PollResult struct {
	Checked     int      `json:"checked"`
	Updated     int      `json:"updated"`
	Events      int      `json:"events"`
	Notified    int      `json:"notified"`
	RateLimited bool     `json:"rate_limited,omitempty"`
	Errors      []string `json:"errors,omitempty"`
}

func Poll(ctx context.Context, d *db.DB, env config.Env, now float64) (PollResult, error) {
	s, err := Load(ctx, d.Read)
	if err != nil {
		return PollResult{}, err
	}
	defer s.Close()
	cutoff := now - float64(s.PollDays)*86400
	rows, err := d.Read.QueryContext(ctx,
		"SELECT id, tracking_number, carrier, status, status_text, aggregator, tracker_id, last_event, last_event_hash, "+
			"notified_status, last_event_ts, last_sync_ts, tracker_registered_ts FROM packages "+
			"WHERE dismissed = 0 AND aggregator = 'ship24' AND tracker_id <> '' AND status <> 'delivered' "+
			"AND updated_ts >= ? ORDER BY last_sync_ts ASC LIMIT ?", cutoff, s.PollLimit)
	if err != nil {
		return PollResult{}, err
	}
	targets, err := db.Collect(rows, func(r *sql.Rows) (Target, error) {
		var t Target
		err := r.Scan(&t.ID, &t.TrackingNumber, &t.Carrier, &t.Status, &t.StatusText, &t.Aggregator, &t.TrackerID,
			&t.LastEvent, &t.LastEventHash, &t.NotifiedStatus, &t.LastEventTS, &t.LastSyncTS, &t.TrackerRegisteredTS)
		return t, err
	})
	if err != nil {
		return PollResult{}, err
	}
	if len(targets) == 0 {
		return PollResult{}, nil
	}
	client, err := clientFor(ctx, d.Read, s)
	if err != nil {
		var missing config.NotConfigured
		if errors.As(err, &missing) {
			return PollResult{}, nil
		}
		return PollResult{}, err
	}
	defer client.Close()
	var out PollResult
	for _, t := range targets {
		out.Checked++
		tracking, err := client.Results(ctx, t.TrackerID)
		if err != nil {
			switch {
			case errors.Is(err, ErrTrackerGone):
				if _, uerr := d.Write.ExecContext(ctx, "UPDATE packages SET aggregator = '', tracker_id = '' WHERE id = ?", t.ID); uerr != nil {
					out.Errors = append(out.Errors, uerr.Error())
				}
			default:
				var limited *RateLimitError
				if errors.As(err, &limited) {
					out.RateLimited = true
					return out, nil
				}
				out.Errors = append(out.Errors, err.Error())
			}
			continue
		}
		notified, err := save(ctx, d, now, t.ID, t.TrackerID, tracking, true, env)
		if err != nil {
			out.Errors = append(out.Errors, err.Error())
			continue
		}
		out.Updated++
		out.Events += len(tracking.Events)
		if notified {
			out.Notified++
		}
	}
	return out, nil
}

func save(ctx context.Context, d *db.DB, now float64, id int64, trackerID string, tracking Tracking, notifyTransition bool, env config.Env) (bool, error) {
	var previous, notifiedStatus string
	if err := d.Read.QueryRowContext(ctx, "SELECT status, notified_status FROM packages WHERE id = ?", id).Scan(&previous, &notifiedStatus); err != nil {
		return false, err
	}
	next := statusOf(tracking)
	advanced := advance(previous, next)
	latest := latestEvent(tracking)
	text := textOf(tracking)
	eventTS := eventTime(latest, now)
	key := eventKey(latest)
	hasData := next != "" || len(tracking.Events) > 0
	notified := false
	enabled := notify.Enabled(ctx)
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range tracking.Events {
			if _, err := tx.ExecContext(ctx,
				"INSERT OR IGNORE INTO package_events (package_id, tracker_id, event_key, event_ts, status, status_code, status_milestone, description, location, created_ts) "+
					"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				id, trackerID, eventKey(e), eventTime(e, now), e.Status, e.StatusCode, e.StatusMilestone, descriptionOf(e), e.Location, now); err != nil {
				return err
			}
		}
		sets := []string{"aggregator = 'ship24'", "tracker_id = ?", "last_sync_ts = ?",
			"tracker_registered_ts = CASE WHEN tracker_registered_ts = 0 THEN ? ELSE tracker_registered_ts END"}
		args := []any{trackerID, now, now}
		if hasData {
			sets = append(sets, "status = ?", "status_text = ?", "last_event = ?", "last_event_ts = ?", "last_event_hash = ?")
			args = append(args, advanced, text, text, eventTS, key)
		}
		notify := notifyTransition && next != "" && (advanced == delivered || advanced == exception) && advanced != previous && advanced != notifiedStatus
		nextNotified := notifiedStatus
		if previous == exception && advanced != exception {
			nextNotified = ""
		}
		if notify {
			nextNotified = advanced
			notified = true
		}
		if nextNotified != notifiedStatus && (!notify || !enabled) {
			sets = append(sets, "notified_status = ?")
			args = append(args, nextNotified)
		}
		_, err := tx.ExecContext(ctx, "UPDATE packages SET "+strings.Join(sets, ", ")+" WHERE id = ?", append(args, id)...)
		return err
	})
	if err != nil {
		return false, err
	}
	if notified && enabled {
		title := "Package delivered"
		if advanced == exception {
			title = "Package exception"
		}
		body := text
		if body == "" {
			body = "Status " + advanced
		}
		if _, err := notify.Send(ctx, d, notify.Message{
			Title: title, Body: body, Channel: notify.Assistant, Now: func() float64 { return now },
			Occurrence: fmt.Sprintf("package:%d:%s:%g", id, advanced, now),
			Mark: func(ctx context.Context, tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, "UPDATE packages SET notified_status = ? WHERE id = ?", advanced, id)
				return err
			},
		}); err != nil {
			log.Warn("package notification failed", logx.Err, err.Error())
		}
	}
	return notified, nil
}

func advance(previous, next string) string {
	if next == "" {
		return previous
	}
	if previous == delivered {
		return delivered
	}
	if next == delivered {
		return delivered
	}
	if previous == exception {
		return next
	}
	if statusRank(next) < statusRank(previous) {
		return previous
	}
	return next
}

func statusRank(status string) int {
	switch status {
	case ordered:
		return 0
	case shipped:
		return 1
	case inTransit:
		return 2
	case outForDelivery, exception:
		return 3
	case delivered:
		return 4
	}
	return -1
}

func statusOf(t Tracking) string {
	switch t.Shipment.StatusMilestone {
	case delivered:
		return delivered
	case exception, "failed_attempt":
		return exception
	case outForDelivery, "available_for_pickup":
		return outForDelivery
	case inTransit:
		return inTransit
	case "info_received":
		return shipped
	}
	return ""
}

func latestEvent(t Tracking) Event {
	if len(t.Events) > 0 {
		return t.Events[0]
	}
	return Event{StatusMilestone: t.Shipment.StatusMilestone, StatusCode: t.Shipment.StatusCode}
}

func textOf(t Tracking) string {
	latest := latestEvent(t)
	if latest.Status != "" {
		return clip(latest.Status, 120)
	}
	return clip(latest.StatusMilestone, 120)
}

func eventTime(e Event, now float64) float64 {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z", "2006-01-02T15:04:05"} {
		if at, err := time.Parse(layout, e.OccurrenceDatetime); err == nil {
			return float64(at.Unix())
		}
	}
	return now
}

func descriptionOf(e Event) string {
	if e.Description != "" {
		return e.Description
	}
	return e.Status
}

func eventKey(e Event) string {
	if e.EventID != "" {
		return e.EventID
	}
	sum := sha256.Sum256([]byte(e.Status + "\x00" + e.OccurrenceDatetime + "\x00" + e.Location))
	return hex.EncodeToString(sum[:])
}

func courierCode(carrier string) string {
	switch strings.ToLower(strings.TrimSpace(carrier)) {
	case "ups":
		return "ups"
	case "usps", "us post", "us-post":
		return "us-post"
	case "fedex":
		return "fedex"
	case "dhl":
		return "dhl"
	}
	return strings.ToLower(strings.TrimSpace(carrier))
}

type Undo struct {
	Op        string `json:"op"`
	PackageID int64  `json:"package_id"`
	TrackerID string `json:"tracker_id"`
	Before    Target `json:"before"`
}

func Unsubscribe(ctx context.Context, q db.Querier, u Undo) (any, error) {
	s, err := Load(ctx, q)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if s.Key == "" {
		return nil, config.NotConfigured("the Ship24 connection has no API key; the Ship24 tracker is still subscribed")
	}
	if u.TrackerID == "" {
		return nil, Error("the tracker id is missing; the Ship24 tracker is still subscribed")
	}
	client, err := clientFor(ctx, q, s)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if err := client.Unsubscribe(ctx, u.TrackerID); err != nil {
		return nil, err
	}
	return map[string]any{"untracked": u.TrackerID}, nil
}

func Restore(ctx context.Context, q db.Querier, u Undo) error {
	if _, err := q.ExecContext(ctx, "DELETE FROM package_events WHERE package_id = ?", u.PackageID); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx,
		"UPDATE packages SET status = ?, status_text = ?, aggregator = ?, tracker_id = ?, last_event = ?, last_event_ts = ?, "+
			"last_event_hash = ?, last_sync_ts = ?, notified_status = ? WHERE id = ?",
		u.Before.Status, u.Before.StatusText, u.Before.Aggregator, u.Before.TrackerID, u.Before.LastEvent, u.Before.LastEventTS,
		u.Before.LastEventHash, u.Before.LastSyncTS, u.Before.NotifiedStatus, u.PackageID)
	return err
}

func Events(ctx context.Context, q db.Querier, packageID int64) ([]Event, error) {
	rows, err := q.QueryContext(ctx,
		"SELECT COALESCE(event_key, ''), status, status_code, status_milestone, description, location, event_ts "+
			"FROM package_events WHERE package_id = ? ORDER BY event_ts DESC LIMIT 10", packageID)
	if err != nil {
		return nil, err
	}
	return db.Collect(rows, func(r *sql.Rows) (Event, error) {
		var e Event
		var ts float64
		err := r.Scan(&e.EventID, &e.Status, &e.StatusCode, &e.StatusMilestone, &e.Description, &e.Location, &ts)
		if err == nil {
			e.OccurrenceDatetime = time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
		}
		return e, err
	})
}
