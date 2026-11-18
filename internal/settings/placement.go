package settings

import "strings"

const (
	PlaceApps               = "apps"
	PlaceAppsCalendar       = "apps.calendar"
	PlaceAppsLibrary        = "apps.library"
	PlaceAppsMail           = "apps.mail"
	PlaceAssistantAuthority = "assistant.authority"
	PlaceAssistantBehavior  = "assistant.behavior"
	PlaceAssistantHelpers   = "assistant.helpers"
	PlaceAssistantRoutines  = "assistant.routines"
	PlaceAssistantSpend     = "assistant.spend"
	PlaceConnections        = "connections"
	PlaceConnectionsWeather = "connections.weather"
	PlaceKnowledgeExport    = "knowledge.export"
	PlacePlanOptions        = "plan.options"
	PlaceSettingsGeneral    = "settings.general"
	PlaceSettingsSignIn     = "settings.sign_in"
	permissionPrefix        = "permission_"
	flagSuffix              = "_enabled"
)

var placement = map[string]string{
	"agent_effort_cron":                   PlaceAssistantRoutines,
	"agent_effort_nightly":                PlaceAssistantRoutines,
	"agent_effort_subagent":               PlaceAssistantHelpers,
	"agent_schedule_min_interval_seconds": PlaceAssistantRoutines,
	"agent_subagent_max_cost_usd":         PlaceAssistantSpend,
	"budget_chat_share":                   PlaceAssistantSpend,
	"budget_unattended_share":             PlaceAssistantSpend,
	"calendar_account":                    PlaceAppsCalendar,
	"default_context_limit":               PlaceAssistantBehavior,
	"default_effort":                      PlaceAssistantBehavior,
	"default_event_minutes":               PlaceAppsCalendar,
	"default_model":                       PlaceAssistantBehavior,
	"digest_notify":                       PlaceAssistantRoutines,
	"dreaming_max_cost_usd":               PlaceAssistantRoutines,
	"dreaming_monthly_usd":                PlaceAssistantRoutines,
	"export_obsidian_kinds":               PlaceKnowledgeExport,
	"export_obsidian_private_to":          PlaceKnowledgeExport,
	"icloud_apple_id":                     PlaceConnections,
	"icloud_contacts_apple_id":            PlaceConnections,
	"icloud_calendar":                     PlaceAppsCalendar,
	"mail_mode":                           PlaceAppsMail,
	"mail_ship24_destination_country":     PlaceConnections,
	"mail_ship24_destination_postcode":    PlaceConnections,
	"max_run_cost_usd":                    PlaceAssistantSpend,
	"media_link_ttl_seconds":              PlaceAppsLibrary,
	"monthly_budget_usd":                  PlaceAssistantSpend,
	"oidc_session_idle_days":              PlaceSettingsSignIn,
	"oidc_session_max_days":               PlaceSettingsSignIn,
	"openloops_remind_ahead_days":         PlacePlanOptions,
	"openloops_remind_hour":               PlacePlanOptions,
	"openloops_snooze_default_days":       PlacePlanOptions,
	"openloops_waiting_after_days":        PlacePlanOptions,
	"ops_worker_max_run_usd":              PlaceAssistantRoutines,
	"ops_worker_monthly_usd":              PlaceAssistantRoutines,
	"search_web_max_cost_usd":             PlaceAssistantSpend,
	"search_web_model":                    PlaceAssistantHelpers,
	"thread_cost_cap_usd":                 PlaceAssistantSpend,
	"tz":                                  PlaceSettingsGeneral,
	"uncertain_dest":                      PlaceAppsMail,
	"weather_latitude":                    PlaceConnectionsWeather,
	"weather_location_name":               PlaceConnectionsWeather,
	"weather_longitude":                   PlaceConnectionsWeather,
	"work_days":                           PlaceAppsCalendar,
	"work_hours":                          PlaceAppsCalendar,
	"write_cap":                           PlaceAppsMail,
}

func Placement(key string) string {
	if place, ok := placement[key]; ok {
		return place
	}
	switch {
	case strings.HasPrefix(key, permissionPrefix):
		return PlaceAssistantAuthority
	case strings.HasSuffix(key, flagSuffix):
		return PlaceApps
	}
	return ""
}
