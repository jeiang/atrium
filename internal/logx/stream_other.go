//go:build !unix

package logx

import "io"

func isJournalStream(io.Writer) bool { return false }
