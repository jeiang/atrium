// Package live is live.py: the live-mode action ledger and go-live state.
package live

const (
	Applied = "applied"
	Skipped = "skipped"
	Undone  = "undone"
	Held    = "interrupted"
)

// StateOf is state_of: the state an auto_actions row's ok and undone_ts columns describe.
func StateOf(ok *int64, undoneTS *float64) string {
	switch {
	case undoneTS != nil:
		return Undone
	case ok != nil && *ok == 1:
		return Applied
	case ok != nil && *ok == 0:
		return Skipped
	}
	return Held
}
