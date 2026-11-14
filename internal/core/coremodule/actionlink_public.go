package coremodule

import (
	"net/http"

	"github.com/jeiang/atrium/internal/actionlink"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/settings"
)

func actionlinkPublic(config.Config, config.Env, *db.DB, *settings.Registry) func(*http.Request) bool {
	return actionlink.PublicCheck()
}
