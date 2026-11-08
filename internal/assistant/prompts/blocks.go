package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/jeiang/atrium/internal/assistant/tool"
	"github.com/jeiang/atrium/internal/py"
)

type CacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

var Ephemeral = CacheControl{Type: "ephemeral"}

const (
	TTL5m = "5m"
	TTL1h = "1h"
)

func PrefixCacheControl(kind tool.Kind, ttl string) CacheControl {
	if kind == tool.Chat && ttl == TTL1h {
		return CacheControl{Type: "ephemeral", TTL: TTL1h}
	}
	return Ephemeral
}

func (c CacheControl) JSON() (json.RawMessage, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("prompts: cache control: %w", err)
	}
	return raw, nil
}

type Block struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *CacheControl `json:"cache_control,omitempty"`
}

type Parts struct {
	System      string
	Profile     string
	SkillsIndex string
	Extra       string
	Project     string
}

func SystemBlocks(p Parts, cc CacheControl) []Block {
	blocks := []Block{{Type: "text", Text: WithPreamble(p.System), CacheControl: &cc}}
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
		d, ok := v.(py.Dict)
		if !ok {
			return "", fmt.Errorf("prompts: tool definition is not an object")
		}
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
