package pages

import (
	"errors"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/media"
)

type errorKind int

const (
	backendError errorKind = iota
	planError
	channelError
	tmdbError
	notFoundError
)

// classifyError is the single place that decides which media failures are
// expected enough to render; everything else is a bug and must panic.
func classifyError(err error) (string, errorKind, bool) {
	var missing config.NotConfigured
	var qbitErr media.QbitError
	var planErr media.PlanError
	var channelErr media.ChannelError
	var eztvErr media.EztvError
	var tmdbErr media.TmdbError
	var gone media.NotFound
	switch {
	case errors.As(err, &missing), errors.As(err, &qbitErr):
		return err.Error(), backendError, true
	case errors.As(err, &planErr):
		return err.Error(), planError, true
	case errors.As(err, &channelErr), errors.As(err, &eztvErr):
		return err.Error(), channelError, true
	case errors.As(err, &tmdbErr):
		return err.Error(), tmdbError, true
	case errors.As(err, &gone):
		return err.Error(), notFoundError, true
	}
	return "", 0, false
}

func mustClassify(err error) (string, errorKind) {
	text, kind, ok := classifyError(err)
	if !ok {
		panic(err)
	}
	return text, kind
}
