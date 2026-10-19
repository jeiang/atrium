package calendar

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/caldav"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/py"
)

type icloudCalendar struct {
	client *caldav.Client
	path   string
}

const homeSetRequest = `<?xml version="1.0" encoding="utf-8"?>` +
	`<d:propfind xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav"><d:prop><c:calendar-home-set/></d:prop></d:propfind>`

type homeSetReply struct {
	Responses []struct {
		Href string `xml:"DAV: href"`
		Prop struct {
			HomeSet struct {
				Href string `xml:"DAV: href"`
			} `xml:"urn:ietf:params:xml:ns:caldav calendar-home-set"`
		} `xml:"DAV: propstat>prop"`
	} `xml:"DAV: response"`
}

func homeSetURL(ctx context.Context, client webdav.HTTPClient, principal *url.URL) (*url.URL, error) {
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", principal.String(), strings.NewReader(homeSetRequest))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	req.Header.Set("Depth", "0")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusMultiStatus {
		return nil, fmt.Errorf("caldav: calendar-home-set: %s", resp.Status)
	}
	var reply homeSetReply
	if err := xml.Unmarshal(body, &reply); err != nil {
		return nil, err
	}
	for _, r := range reply.Responses {
		if href := strings.TrimSpace(r.Prop.HomeSet.Href); href != "" {
			ref, err := url.Parse(href)
			if err != nil {
				return nil, err
			}
			return principal.ResolveReference(ref), nil
		}
	}
	return nil, errors.New("caldav: no calendar-home-set")
}

func openICloud(ctx context.Context, env config.Env) (*icloudCalendar, error) {
	login, err := config.ICloudCalDAV(env)
	if err != nil {
		return nil, NotConfigured(err.Error())
	}
	httpClient := webdav.HTTPClientWithBasicAuth(&http.Client{Timeout: remoteTimeout}, login.AppleID, login.Password)
	base, err := url.Parse(endpoints.CalDAV)
	if err != nil {
		return nil, err
	}
	first, err := caldav.NewClient(httpClient, base.String())
	if err != nil {
		return nil, remote(err)
	}
	principalPath, err := first.FindCurrentUserPrincipal(ctx)
	if err != nil {
		return nil, remote(err)
	}
	home, err := homeSetURL(ctx, httpClient, base.ResolveReference(&url.URL{Path: principalPath}))
	if err != nil {
		return nil, remote(err)
	}
	host := &url.URL{Scheme: home.Scheme, Host: home.Host}
	client, err := caldav.NewClient(httpClient, host.String())
	if err != nil {
		return nil, remote(err)
	}
	calendars, err := client.FindCalendars(ctx, home.Path)
	if err != nil {
		return nil, remote(err)
	}
	named := config.ICloudCalendar(env)
	for _, cal := range calendars {
		if named != "" && py.Lower(cal.Name) == named {
			return &icloudCalendar{client, cal.Path}, nil
		}
	}
	if named != "" {
		return nil, Error(fmt.Sprintf("iCloud calendar %s not found", py.Repr(named)))
	}
	for _, cal := range calendars {
		if len(cal.SupportedComponentSet) == 0 || slices.Contains(cal.SupportedComponentSet, "VEVENT") {
			return &icloudCalendar{client, cal.Path}, nil
		}
	}
	return nil, Error("no iCloud event calendar found")
}

func (c *icloudCalendar) objectPath(uid string) string {
	return strings.TrimRight(c.path, "/") + "/" + url.PathEscape(strings.ReplaceAll(uid, "/", "%2F")) + ".ics"
}

func (c *icloudCalendar) save(ctx context.Context, uid string, cal *ical.Calendar) error {
	_, err := c.client.PutCalendarObject(ctx, c.objectPath(uid), cal)
	return err
}

func (c *icloudCalendar) remove(ctx context.Context, uid string) error {
	query := &caldav.CalendarQuery{
		CompRequest: caldav.CalendarCompRequest{Name: "VCALENDAR", AllProps: true, AllComps: true},
		CompFilter: caldav.CompFilter{Name: "VCALENDAR", Comps: []caldav.CompFilter{{
			Name:  "VEVENT",
			Props: []caldav.PropFilter{{Name: "UID", TextMatch: &caldav.TextMatch{Text: uid}}},
		}}},
	}
	objects, err := c.client.QueryCalendar(ctx, c.path, query)
	if err != nil {
		return err
	}
	for _, object := range objects {
		if !hasUID(object.Data, uid) {
			continue
		}
		if err := c.client.RemoveAll(ctx, object.Path); err != nil {
			if strings.HasPrefix(err.Error(), "404 ") {
				return nil
			}
			return err
		}
		return nil
	}
	return nil
}

func hasUID(cal *ical.Calendar, uid string) bool {
	for _, child := range cal.Children {
		if child.Name != ical.CompEvent {
			continue
		}
		if p := child.Props.Get(ical.PropUID); p != nil && unescapeText(p.Value) == uid {
			return true
		}
	}
	return false
}
