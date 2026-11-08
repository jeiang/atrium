package ledger

type State string

const (
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

func BlockedSQL(alias string) string {
	return alias + ".state IN ('unknown', 'partial', 'undo_unknown', 'undo_partial')"
}

func ActiveSQL(alias string) string { return alias + ".state IN ('held', 'unknown', 'partial')" }

func UnresolvedSQL(alias string) string {
	return "EXISTS (SELECT 1 FROM proposal_actions " + alias + " WHERE " + alias + ".proposal_id = proposals.id AND " + ActiveSQL(alias) + ")"
}

var MailUnresolvedSQL = "(EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.uids') u WHERE " + ActiveSQL("pa") +
	" AND u.value = m.uid AND COALESCE(json_extract(pa.action, '$.src_uidvalidity'), json_extract(pa.action, '$.uidvalidity')) = m.uidvalidity " +
	"AND COALESCE(json_extract(pa.action, '$.src_imap'), json_extract(pa.action, '$.imap_name')) = mb.imap_name) OR " +
	"EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.copied') u WHERE " + ActiveSQL("pa") + " AND " +
	"u.key = CAST(m.uid AS TEXT) AND json_extract(pa.action, '$.src_uidvalidity') = m.uidvalidity " +
	"AND json_extract(pa.action, '$.src_imap') = mb.imap_name))"
