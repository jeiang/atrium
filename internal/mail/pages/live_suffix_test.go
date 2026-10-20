package pages

import (
	"errors"
	"net/url"
	"testing"

	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func TestUICorrectionReportsAFailedMove(t *testing.T) {
	w := liveWorld(t)
	w.Fake.FailWrite = func(mailtest.FakeWrite) error { return errors.New("NO try later") }
	s := liveSite(t, w, nil)
	body := status(t, s.post("/messages/"+s.id(w.MID("fix-me"))+"/correct", url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "failed")
	contains(t, body, "Correction recorded", "It could not be moved; see Decisions.")
	lacks(t, body, "The message was moved.")
}

func TestUICorrectionReportsASkippedMove(t *testing.T) {
	w := liveWorld(t)
	id := w.MID("fix-me")
	for uid := range w.InboxBox().Messages {
		w.InboxBox().Remove(uid)
	}
	s := liveSite(t, w, nil)
	body := status(t, s.post("/messages/"+s.id(id)+"/correct", url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "skipped")
	contains(t, body, "Correction recorded", "It could not be moved; see Decisions.")
	if len(w.Fake.Writes) != 0 {
		t.Errorf("a skipped correction wrote %v", w.Fake.Writes)
	}
}
