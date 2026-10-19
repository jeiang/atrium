package imapclient

import (
	"github.com/emersion/go-imap/v2"
)

// handleVanished reads `VANISHED [(EARLIER)] <uid-set>` (RFC 7162). iCloud
// sends it after UID EXPUNGE although QRESYNC was never enabled. It carries
// UIDs, not sequence numbers, so it only lowers the message count.
func (c *Client) handleVanished() error {
	if !c.dec.ExpectSP() {
		return c.dec.Err()
	}
	earlier := false
	if c.dec.Special('(') {
		var tag string
		if !c.dec.ExpectAtom(&tag) || !c.dec.ExpectSpecial(')') || !c.dec.ExpectSP() {
			return c.dec.Err()
		}
		earlier = true
	}
	var uids imap.UIDSet
	if !c.dec.ExpectUIDSet(&uids) {
		return c.dec.Err()
	}
	if earlier {
		return nil
	}
	n, ok := uids.Nums()
	if !ok {
		return nil
	}
	c.mutex.Lock()
	if c.state == imap.ConnStateSelected && c.mailbox != nil {
		c.mailbox = c.mailbox.copy()
		if uint32(len(n)) >= c.mailbox.NumMessages {
			c.mailbox.NumMessages = 0
		} else {
			c.mailbox.NumMessages -= uint32(len(n))
		}
	}
	c.mutex.Unlock()
	return nil
}
