package module

import (
	"github.com/jeiang/atrium/internal/assistant/registry"
	"github.com/jeiang/atrium/internal/tool"
)

type ToolDefinition struct {
	Tool tool.Tool
	Bind registry.Binder
}
