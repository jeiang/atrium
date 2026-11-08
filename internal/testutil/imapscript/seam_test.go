package imapscript_test

import (
	"context"
	"testing"

	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/testutil/imapscript"
)

func TestIMAPEndpointSeam(t *testing.T) {
	srv, ca := imapscript.ServeTLS(t, imapscript.MustParse(t, `
S: * OK [CAPABILITY IMAP4rev1 SASL-IR] fake
C: {t} LOGIN "atrium.test@icloud.com" "app password"
S: {t} OK [CAPABILITY IMAP4rev1 SASL-IR] logged in
C: {t} LIST "" "*"
S: * LIST (\Noselect) "/" "/"
S: {t} OK LIST completed
C: {t} LOGOUT
S: * BYE
S: {t} OK
`))
	t.Setenv("ATRIUM_IMAP_ADDR", srv.Addr())
	t.Setenv("ATRIUM_IMAP_CA", ca)

	c := imapclient.New("imap.mail.me.com", "atrium.test@icloud.com", "app password")
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect through ATRIUM_IMAP_ADDR: %v", err)
	}
	boxes, err := c.ListMailboxes(context.Background())
	if err != nil {
		t.Fatalf("ListMailboxes: %v", err)
	}
	if len(boxes) != 1 || boxes[0].Name != "/" {
		t.Fatalf("ListMailboxes: got %#v", boxes)
	}
	c.Close()
}
