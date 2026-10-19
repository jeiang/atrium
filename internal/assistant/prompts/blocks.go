package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

// CacheControl is a cache_control object: ephemeral, with a ttl only when it is not the 5m default.
type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

// Ephemeral is CACHE_CONTROL, the 5m breakpoint.
var Ephemeral = CacheControl{Type: "ephemeral"}

// CacheTTLs are the values of the cache_ttl_chat setting.
const (
	TTL5m = "5m"
	TTL1h = "1h"
)

// PrefixCacheControl is prefix_cache_control: the breakpoint of the cached prefix. Only chat honours the
// setting ttl (an unknown value is 5m); cron, nightly and subagent runs always cache for 5m.
func PrefixCacheControl(kind tool.Kind, ttl string) CacheControl {
	if kind == tool.Chat && ttl == TTL1h {
		return CacheControl{Type: "ephemeral", TTL: TTL1h}
	}
	return Ephemeral
}

// JSON is the cache_control value as a request part.
func (c CacheControl) JSON() json.RawMessage {
	raw, _ := json.Marshal(c)
	return raw
}

// Block is one system text block.
type Block struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

// Parts are the variable texts after System; an empty one is left out.
type Parts struct {
	Profile     string
	SkillsIndex string
	Extra       string
	Project     string
}

// SystemBlocks is loop.system_blocks: System, profile, skills index, role addendum, then the project block.
// Breakpoints sit on System and on the last of the stable blocks; the project block follows uncached because it
// varies per thread.
func SystemBlocks(p Parts, cc CacheControl) []Block {
	blocks := []Block{{Type: "text", Text: System, CacheControl: &cc}}
	for _, text := range []string{p.Profile, p.SkillsIndex, p.Extra} {
		if text != "" {
			blocks = append(blocks, Block{Type: "text", Text: text})
		}
	}
	if len(blocks) > 1 {
		blocks[len(blocks)-1].CacheControl = &cc
	}
	if p.Project != "" {
		blocks = append(blocks, Block{Type: "text", Text: p.Project})
	}
	return blocks
}

// Signature is system_signature: what signed thinking blocks are bound to, the system texts and the tool
// definitions without their cache_control, as 16 hex digits of a sha256.
func Signature(system []Block, tools []json.RawMessage) (string, error) {
	texts := make([]any, len(system))
	for i, b := range system {
		texts[i] = b.Text
	}
	defs := make([]any, len(tools))
	for i, raw := range tools {
		v, err := py.JSONLoads(raw)
		if err != nil {
			return "", err
		}
		d, _ := v.(py.Dict)
		clean := make(py.Dict, 0, len(d))
		for _, pair := range d {
			if pair.Key != "cache_control" {
				clean = append(clean, pair)
			}
		}
		defs[i] = clean
	}
	sum := sha256.Sum256([]byte(py.JSONDumps([]any{texts, defs}, py.JSONOpts{SortKeys: true})))
	return hex.EncodeToString(sum[:])[:16], nil
}
