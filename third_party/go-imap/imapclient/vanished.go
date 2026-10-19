package imapclient

import (
	"github.com/emersion/go-imap/v2"
)

// handleVanished reads and discards `VANISHED [(EARLIER)] <uid-set>` (RFC 7162).
// iCloud sends it after UID EXPUNGE although QRESYNC was never enabled. It
// carries UIDs, not sequence numbers, and the set is never expanded.
func (c *Client) handleVanished() error {
	if !c.dec.ExpectSP() {
		return c.dec.Err()
	}
	if c.dec.Special('(') {
		var tag string
		if !c.dec.ExpectAtom(&tag) || !c.dec.ExpectSpecial(')') || !c.dec.ExpectSP() {
			return c.dec.Err()
		}
	}
	var uids imap.UIDSet
	if !c.dec.ExpectUIDSet(&uids) {
		return c.dec.Err()
	}
	return nil
}
