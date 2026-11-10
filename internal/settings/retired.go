package settings

import "strings"

type RetiredKey struct {
	Module string
	Key    string
}

var Retired = []RetiredKey{
	{Module: "assistant", Key: "agent_profile_key_chars"},
	{Module: "assistant", Key: "agent_profile_value_chars"},
	{Module: "assistant", Key: "nightly_stale_months"},
	{Module: "notion", Key: "notion_link_page_chars"},
	{Module: "notion", Key: "notion_edit_page_chars"},
	{Module: "notion", Key: "notion_link_max_tokens"},
	{Module: "notion", Key: "notion_edit_max_tokens"},
	{Module: "assistant", Key: "agent_notion_max_markdown_chars"},
	{Module: "assistant", Key: "agent_notion_max_results"},
	{Module: "assistant", Key: "agent_notion_page_chars"},
	{Module: "assistant", Key: "agent_notion_snippet_chars"},
	{Module: "assistant", Key: "agent_notion_stale_seconds"},
	{Module: "assistant", Key: "agent_context_max_ids"},
	{Module: "assistant", Key: "agent_context_max_sources"},
	{Module: "assistant", Key: "agent_context_summary_chars"},
	{Module: "assistant", Key: "agent_effort_sub"},
	{Module: "web", Key: "learned_limit"},
	{Module: "web", Key: "loop_limit"},
	{Module: "web", Key: "activity_page_size"},
	{Module: "web", Key: "activity_snippet_chars"},
	{Module: "web", Key: "page_size"},
	{Module: "web", Key: "run_history"},
	{Module: "web_search", Key: "dynamic_filtering"},
	{Module: "memory", Key: "backfill_done"},
	{Module: "memory", Key: "node_bytes"},
	{Module: "memory", Key: "model"},
	{Module: "memory", Key: "effort"},
	{Module: "memory", Key: "concurrency"},
	{Module: "memory", Key: "view_low"},
	{Module: "memory", Key: "view_high"},
	{Module: "memory", Key: "tail_messages"},
	{Module: "memory", Key: "verbatim_tokens"},
	{Module: "anthropic", Key: "anthropic_admin_key"},
	{Module: "openai", Key: "openai_admin_key"},
}

func RetiredAt(module, key string) (RetiredKey, bool) {
	for _, r := range Retired {
		if r.Module == module && r.Key == key {
			return r, true
		}
	}
	return RetiredKey{}, false
}

func (r RetiredKey) EnvName() string {
	return "ATRIUM_" + strings.ToUpper(r.Module) + "_" + strings.ToUpper(r.Key)
}
