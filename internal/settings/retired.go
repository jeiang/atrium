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
