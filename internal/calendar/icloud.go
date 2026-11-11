package calendar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
)

type icloudCalendar struct {
	client *davmirror.Client
	path   *url.URL
}

func davHTTP(ctx context.Context, set Settings) (*http.Client, error) {
	login, err := ICloudLogin(ctx, set)
	if err != nil {
		return nil, err
	}
	base := &http.Client{Timeout: remoteTimeout}
	if set.DAV != nil {
		if client, err := set.DAV.Transport(); err == nil {
			base = client
		}
	}
	return davmirror.BasicClient(base, login.AppleID, login.Password), nil
}

func icloudCalendars(ctx context.Context, env config.Env, set Settings) (*davmirror.Client, []davmirror.CollectionRef, error) {
	httpClient, err := davHTTP(ctx, set)
	if err != nil {
		return nil, nil, err
	}
	client, err := davmirror.New(httpClient, EndpointsFrom(env).CalDAV)
	if err != nil {
		return nil, nil, remote(err)
	}
	collections, err := client.Collections(ctx, davmirror.Calendars)
	if err != nil {
		return nil, nil, remote(err)
	}
	return client, collections, nil
}

func openICloud(ctx context.Context, env config.Env, set Settings) (*icloudCalendar, error) {
	client, collections, err := icloudCalendars(ctx, env, set)
	if err != nil {
		return nil, err
	}
	named := set.ICloudCalendar
	for _, cal := range collections {
		if named != "" && strings.ToLower(cal.Name) == named {
			return &icloudCalendar{client, cal.URL}, nil
		}
	}
	if named != "" {
		return nil, Error(fmt.Sprintf("iCloud calendar %q not found", named))
	}
	for _, cal := range collections {
		if len(cal.Components) == 0 || slices.Contains(cal.Components, "VEVENT") {
			return &icloudCalendar{client, cal.URL}, nil
		}
	}
	return nil, Error("no iCloud event calendar found")
}

func (c *icloudCalendar) objectPath(uid string) *url.URL {
	u := *c.path
	u.Path = strings.TrimRight(u.Path, "/") + "/" + uid + ".ics"
	u.RawPath = ""
	return &u
}

func (c *icloudCalendar) save(ctx context.Context, uid string, cal *ical.Calendar) error {
	body, err := encodeICS(cal)
	if err != nil {
		return err
	}
	return c.client.Put(ctx, c.objectPath(uid), []byte(body), "text/calendar; charset=utf-8")
}

func (c *icloudCalendar) remove(ctx context.Context, uid string) error {
	objects, err := c.client.Query(ctx, c.path, davmirror.Calendars, uid)
	if err != nil {
		return err
	}
	for _, object := range objects {
		cal, err := decodeICS(object.Body)
		if err != nil || !hasUID(cal, uid) {
			continue
		}
		if err := c.client.Delete(ctx, object.URL); err != nil {
			var status *davmirror.StatusError
			if errors.As(err, &status) && status.Status == http.StatusNotFound {
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
