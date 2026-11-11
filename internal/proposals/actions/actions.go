package actions

import "github.com/jeiang/atrium/internal/ledger"

func Unresolved(alias string) string {
	return "EXISTS (SELECT 1 FROM proposal_actions " + alias + " WHERE " + alias + ".proposal_id = proposals.id AND " + ledger.ActiveSQL(alias) + ")"
}

var MailUnresolved = "(EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.uids') u WHERE " + ledger.ActiveSQL("pa") +
	" AND u.value = m.uid AND COALESCE(json_extract(pa.action, '$.src_uidvalidity'), json_extract(pa.action, '$.uidvalidity')) = m.uidvalidity " +
	"AND COALESCE(json_extract(pa.action, '$.src_imap'), json_extract(pa.action, '$.imap_name')) = mb.imap_name) OR " +
	"EXISTS (SELECT 1 FROM proposal_actions pa, json_each(pa.action, '$.copied') u WHERE " + ledger.ActiveSQL("pa") + " AND " +
	"u.key = CAST(m.uid AS TEXT) AND json_extract(pa.action, '$.src_uidvalidity') = m.uidvalidity " +
	"AND json_extract(pa.action, '$.src_imap') = mb.imap_name))"
