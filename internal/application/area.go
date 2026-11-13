package application

import "slices"

type Area string

var Areas = []Area{
	"home", "threads", "composer", "files", "projects", "review", "proposals", "permissions", "drafts", "activity", "knowledge",
	"tasks", "schedules", "mail", "media", "wiki", "inbox", "devices", "settings", "integrations", "usage", "search",
}

func (a Area) Valid() bool { return slices.Contains(Areas, a) }
