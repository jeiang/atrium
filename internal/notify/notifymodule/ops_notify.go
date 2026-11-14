package notifymodule

import (
	"github.com/jeiang/atrium/internal/application"
	notifyapp "github.com/jeiang/atrium/internal/notify/application"
)

func notificationOperations() ([]application.Definition, []application.Binding) {
	uses := func(name application.Name) []application.Name { return []application.Name{name} }
	return notifyapp.Describe(), append(notificationRoutes(nil, nil), []application.Binding{
		{Kind: application.HTML, Target: "GET /settings/notifications", Uses: uses(notifyapp.ConfigurationName)},
		{Kind: application.HTML, Target: "GET /inbox", Uses: uses(notifyapp.InboxName)},
		{Kind: application.HTML, Target: "POST /settings/notifications/channels/{name}/content", Uses: uses(notifyapp.ChannelContentName)},
		{Kind: application.HTML, Target: "POST /settings/notifications/push/subscriptions", Uses: uses(notifyapp.SubscribeName)},
		{Kind: application.HTML, Target: "POST /settings/notifications/push/unsubscribe", Uses: uses(notifyapp.UnsubscribeName)},
		{Kind: application.HTML, Target: "POST /settings/notifications/push/{id}/remove", Uses: uses(notifyapp.UnsubscribeName)},
		{Kind: application.HTML, Target: "POST /settings/notifications/push/test", Uses: uses(notifyapp.TestName)},
		{Kind: application.HTML, Target: "POST /inbox/{id}/resend", Uses: uses(notifyapp.ResendName)},
		{Kind: application.HTML, Target: "POST /settings/phone-actions", Uses: uses(notifyapp.PhonePolicyName)},
		{Kind: application.HTML, Target: "POST /settings/phone-actions/revoke", Uses: uses(notifyapp.RevokeLinksName)},
		{Kind: application.Tool, Target: "notify", Uses: uses(notifyapp.SendName)},
		{Kind: application.Service, Target: "notifications-monitor", Uses: uses(notifyapp.SendName)},
	}...)
}
