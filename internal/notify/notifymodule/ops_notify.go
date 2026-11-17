package notifymodule

import (
	"github.com/jeiang/atrium/internal/application"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
)

func notificationOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return notifyapp.Describe(), append(notificationRoutes(nil, nil), []application.Binding{
		{Kind: application.Tool, Target: "notify", Uses: uses(notifyapp.SendName)},
		{Kind: application.Service, Target: "notifications-monitor", Uses: uses(notifyapp.SendName)},
	}...)
}
