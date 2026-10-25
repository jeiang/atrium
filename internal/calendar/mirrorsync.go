package calendar

import (
	"context"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
)

func icloudMirror(ctx context.Context, env config.Env, set Settings, endpoint, dir string, k davmirror.Kind) (davmirror.Stats, error) {
	if dir == "" {
		return davmirror.Stats{}, Error("the mirror directory is not set")
	}
	login, err := icloudLogin(env, set)
	if err != nil {
		return davmirror.Stats{}, err
	}
	client := davmirror.BasicClient(&http.Client{Timeout: remoteTimeout}, login.AppleID, login.Password)
	collections, err := davmirror.Fetch(ctx, client, endpoint, k)
	if err != nil {
		return davmirror.Stats{}, err
	}
	return davmirror.Apply(dir, k.Ext, collections)
}

func SyncCalendarMirror(ctx context.Context, env config.Env, set Settings) (davmirror.Stats, error) {
	return icloudMirror(ctx, env, set, endpoints.CalDAV, set.CalendarDir, davmirror.Calendars)
}

func SyncContactsMirror(ctx context.Context, env config.Env, set Settings, dir string) (davmirror.Stats, error) {
	return icloudMirror(ctx, env, set, endpoints.CardDAV, dir, davmirror.Contacts)
}
