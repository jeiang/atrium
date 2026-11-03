# Assistant tools

Generated from the production tool registry by `just toolsgen`. Do not edit by hand.

Run kinds are those that offer the tool after the run-kind policy. A tool that has an `enabled` setting stays listed when the operator turns it off. The flags follow the Rule of Two: untrusted output (U), private data (P) and external effect (E). A thread that read private data loses its external-effect tools.

| Tool | Run kinds | Effect | U | P | E | Can be disabled |
|---|---|---|---|---|---|---|
| `atrium_self_diagnose` | chat, cron, sub | read | yes | no | no | yes |
| `calendar_create` | chat | write_undo | no | no | no | yes |
| `calendar_delete` | chat | write_undo | no | no | no | yes |
| `calendar_list` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `calendar_rsvp` | chat, cron | propose | no | no | no | yes |
| `calendar_update` | chat, cron | propose | no | no | no | yes |
| `commitments` | chat, cron, sub | read | yes | yes | no | yes |
| `contacts_lookup` | chat, cron, nightly, sub | read | no | yes | no | yes |
| `context_drop` | chat, cron, nightly | read | no | no | no | yes |
| `context_fold` | chat, cron, nightly | read | no | no | no | yes |
| `context_pin` | chat, cron, nightly | read | no | no | no | yes |
| `context_status` | chat, cron, nightly | read | no | no | no | yes |
| `draft_email_create` | chat | write_undo | no | yes | no | yes |
| `draft_email_update` | chat | write_undo | no | yes | no | yes |
| `event_propose` | chat, cron | propose | no | no | no | yes |
| `fetch_knowledge` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `find_slot` | chat, cron, nightly, sub | read | no | yes | no | yes |
| `grafana_alerts` | chat, cron, sub | read | yes | no | no | yes |
| `grafana_datasources` | chat, cron, sub | read | yes | no | no | yes |
| `grafana_query` | chat, cron, sub | read | yes | no | no | yes |
| `grafana_search_dashboards` | chat, cron, sub | read | yes | no | no | yes |
| `load_skill` | chat, cron, nightly | read | no | no | no | yes |
| `mail_read` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `mail_search` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `media_add_magnet` | chat | write_undo | no | no | no | yes |
| `media_channel_add` | chat | write_undo | no | no | no | yes |
| `media_channel_remove` | chat | write_undo | no | no | no | yes |
| `media_channels` | chat, cron, sub | read | no | yes | no | yes |
| `media_downloads` | chat, cron, sub | read | yes | yes | no | yes |
| `media_library_search` | chat, cron, sub | read | yes | yes | no | yes |
| `notify` | chat, cron, nightly | notify | no | no | no | yes |
| `notion_add_alias` | chat, nightly | write_undo | no | no | no | yes |
| `notion_archive_page` | chat, nightly | write_undo | no | no | no | yes |
| `notion_create_page` | chat, nightly | write_undo | no | no | no | yes |
| `notion_move_page` | chat, nightly | write_undo | no | no | no | yes |
| `notion_update_page` | chat, nightly | write_undo | no | no | no | yes |
| `open_loops` | chat, cron, sub | read | yes | yes | no | yes |
| `packages` | chat, cron, sub | read | yes | yes | no | yes |
| `plan` | chat, cron, nightly | read | no | no | no | yes |
| `profile_get` | chat, cron, nightly | read | no | yes | no | yes |
| `profile_set` | chat, nightly | write_undo | no | no | no | yes |
| `propose` | chat, cron | propose | no | no | no | yes |
| `recall` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `research` | chat, cron | read | yes | no | no | yes |
| `schedule_cancel` | chat | write_undo | no | no | no | yes |
| `schedule_create` | chat | write_undo | no | no | no | yes |
| `schedule_list` | chat | read | no | no | no | yes |
| `schedule_update` | chat | write_undo | no | no | no | yes |
| `search_knowledge` | chat, cron, nightly, sub | read | yes | yes | no | yes |
| `threads_today` | nightly | read | yes | yes | no | yes |
| `upcoming_birthdays` | chat, cron, sub | read | yes | yes | no | yes |
| `web_fetch` | chat, cron, sub | read | yes | no | yes | yes |
| `web_search` | chat, cron, sub | read | yes | no | yes | yes |
