package calendar

import (
	"context"
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/davmirror"
)

func MirrorConfigured(ctx context.Context, set Settings, dir string) error {
	if dir == "" {
		return config.NotConfigured("the mirror directory is not set")
	}
	if _, err := ICloudLogin(ctx, set); err != nil {
		if missing, ok := errors.AsType[NotConfigured](err); ok {
			return config.NotConfigured(missing)
		}
		return err
	}
	return nil
}

func icloudMirror(ctx context.Context, set Settings, endpoint, dir string, k davmirror.Kind) (davmirror.Stats, error) {
	if dir == "" {
		return davmirror.Stats{}, Error("the mirror directory is not set")
	}
	client, err := davHTTP(ctx, set)
	if err != nil {
		return davmirror.Stats{}, err
	}
	collections, err := davmirror.Fetch(ctx, client, endpoint, k)
	if err != nil {
		return davmirror.Stats{}, err
	}
	return davmirror.Apply(dir, k.Ext, collections)
}

func SyncCalendarMirror(ctx context.Context, env config.Env, set Settings) (davmirror.Stats, error) {
	return icloudMirror(ctx, set, EndpointsFrom(env).CalDAV, set.CalendarDir, davmirror.Calendars)
}

func SyncContactsMirror(ctx context.Context, env config.Env, set Settings, dir string) (davmirror.Stats, error) {
	return icloudMirror(ctx, set, EndpointsFrom(env).CardDAV, dir, davmirror.Contacts)
}
