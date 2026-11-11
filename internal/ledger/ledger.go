package ledger

type State string

const (
	Intent      State = "intent"
	Done        State = "done"
	Held        State = "held"
	Applied     State = "applied"
	OK          State = "ok"
	Failed      State = "failed"
	Skipped     State = "skipped"
	Unknown     State = "unknown"
	Partial     State = "partial"
	Interrupted State = "interrupted"
	Settled     State = "settled"
	Undone      State = "undone"
	UndoHeld    State = "undo_held"
	UndoFailed  State = "undo_failed"
	UndoUnknown State = "undo_unknown"
	UndoPartial State = "undo_partial"
	UndoSettled State = "undo_settled"
)

func (s State) String() string { return string(s) }

func (s State) Held() bool { return s == Held || s == UndoHeld }

func (s State) Blocked() bool {
	return s == Unknown || s == Partial || s == UndoUnknown || s == UndoPartial
}

func (s State) UndoBlocked() bool { return s == UndoUnknown || s == UndoPartial }

func (s State) Settled() bool { return s == Settled || s == UndoSettled || s == Undone }

func (s State) Terminal() bool { return !s.Held() }

func (s State) Final() bool {
	return s == Done || s == Failed || s == Skipped || s == Partial || s == Unknown
}

func (s State) Blocking() bool { return s == Intent || s == Unknown || s == Partial }

func ActiveSQL(alias string) string { return alias + ".state IN ('held', 'unknown', 'partial')" }
