package application

import "github.com/jeiang/atrium/internal/kb"

const UndoOp = kb.UndoOp

func Cite(id int64) string { return kb.Cite(id) }
