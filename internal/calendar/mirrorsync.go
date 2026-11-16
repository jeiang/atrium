package calendar

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/connections"
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

func icloudMirror(ctx context.Context, set Settings, endpoint, dir string, k davmirror.Kind, capability string) (davmirror.Stats, error) {
	if dir == "" {
		return davmirror.Stats{}, Error("the mirror directory is not set")
	}
	client, err := davHTTP(ctx, set)
	if err != nil {
		return davmirror.Stats{}, err
	}
	collections, err := davmirror.Fetch(ctx, client, endpoint, k)
	if result, ok := davOutcome(capability, err); ok && set.DAV != nil && ctx.Err() == nil {
		set.DAV.Report(result)
	}
	if err != nil {
		return davmirror.Stats{}, err
	}
	return davmirror.Apply(dir, k.Ext, collections)
}

func davOutcome(capability string, err error) (connections.Result, bool) {
	if err == nil {
		return connections.Result{Capability: capability, State: connections.OK, Reason: "the mirror sync works"}, true
	}
	if status, ok := errors.AsType[*davmirror.StatusError](err); ok {
		switch {
		case status.Status == http.StatusUnauthorized || status.Status == http.StatusForbidden:
			return connections.Result{Capability: capability, State: connections.NeedsReauth, Reason: "iCloud refused the app password"}, true
		case status.Status >= http.StatusInternalServerError:
			return connections.Result{Capability: capability, State: connections.Failing, Reason: err.Error()}, true
		}
		return connections.Result{}, false
	}
	if errors.As(err, new(net.Error)) || errors.As(err, new(*tls.CertificateVerificationError)) ||
		errors.As(err, new(tls.AlertError)) || errors.As(err, new(tls.RecordHeaderError)) {
		return connections.Result{Capability: capability, State: connections.Failing, Reason: err.Error()}, true
	}
	return connections.Result{}, false
}

func SyncCalendarMirror(ctx context.Context, env config.Env, set Settings) (davmirror.Stats, error) {
	return icloudMirror(ctx, set, EndpointsFrom(env).CalDAV, set.CalendarDir, davmirror.Calendars, CapabilityCalDAV)
}

func SyncContactsMirror(ctx context.Context, env config.Env, set Settings, dir string) (davmirror.Stats, error) {
	return icloudMirror(ctx, set, EndpointsFrom(env).CardDAV, dir, davmirror.Contacts, CapabilityCardDAV)
}
