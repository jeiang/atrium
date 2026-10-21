package pages

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"testing"

	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/mail"
	"github.com/jeiang/atrium/internal/mail/applier"
	"github.com/jeiang/atrium/internal/mail/live/livetest"
	"github.com/jeiang/atrium/internal/mail/mailtest"
)

func liveSite(t *testing.T, w *livetest.World, open func(context.Context, string) (mail.Client, error)) *site {
	t.Helper()
	env := config.Map(map[string]string{"ATRIUM_MODE": "live", "ATRIUM_DB": w.Cfg.DBPath, "ATRIUM_CACHE_DIR": w.Cfg.CacheDir})
	if open == nil {
		open = w.Open
	}
	s := &site{t: t, db: w.DB, cfg: w.Cfg, env: env, srv: newServer(t, w.Cfg, env, w.DB, Options{Open: open, Now: func() float64 { return w.Clock }})}
	s.handshake()
	return s
}

func liveWorld(t *testing.T) *livetest.World {
	t.Helper()
	w := livetest.ICloud(t, "live")
	bills, misc := "Bills", "Misc"
	w.Arrive("shown")
	w.Decide("shown", "file", &bills, "operator")
	w.Arrive("fix-me")
	w.Decide("fix-me", "file", &misc, "operator")
	return w
}

func TestTriageShowsStatusUndoAndHeaderControls(t *testing.T) {
	w := liveWorld(t)
	if _, err := applier.New(w.Live, w.Open, w.Syncer).ApplyAccount(context.Background(), "icloud"); err != nil {
		t.Fatal(err)
	}
	var action = -1
	for i, a := range w.Actions() {
		if a.Kind == "file" && action < 0 {
			action = i
		}
	}
	id := w.Actions()[action].ID
	s := liveSite(t, w, nil)
	page := status(t, s.get("/triage?account=icloud"), 200, "triage")
	contains(t, page, "Applied", "/live/actions/"+s.id(id)+"/undo", "Pause", "Live", `hx-confirm="Undo this and put the message back?"`)
	lacks(t, page, "routing is shadow")
	undone := status(t, s.post("/live/actions/"+s.id(id)+"/undo", nil, hx), 200, "undo")
	contains(t, undone, "Undone", "Undone</h2>", "hx-swap-oob")
	lacks(t, undone, "/undo")
	again := status(t, s.post("/live/actions/"+s.id(id)+"/undo", nil, hx), 200, "undo again")
	contains(t, again, "Not undone", "cannot be undone")
	status(t, s.post("/live/actions/999/undo", nil, hx), 404, "unknown action")
	status(t, s.post("/live/actions/x/undo", nil, hx), 422, "bad action id")
}

func TestUICorrectionMovesTheMessageInLiveModeAndDefersWhenPaused(t *testing.T) {
	w := liveWorld(t)
	s := liveSite(t, w, nil)
	id := w.MID("fix-me")
	path := "/messages/" + s.id(id) + "/correct"
	if err := w.Live.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	held := status(t, s.post(path, url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "paused")
	contains(t, held, "Moving is paused; it will move after you resume.")
	if len(w.Fake.Writes) != 0 {
		t.Errorf("a paused correction wrote %v", w.Fake.Writes)
	}
	if err := w.Live.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	moved := status(t, s.post(path, url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "moved")
	contains(t, moved, "The message was moved.")
	if got := w.Fake.Boxes["Bills"].UIDs(); !slices.Equal(got, []uint32{1}) {
		t.Errorf("Bills = %v", got)
	}
}

func TestUICorrectionFromTheCardShowsTheMoveInline(t *testing.T) {
	w := liveWorld(t)
	s := liveSite(t, w, nil)
	card := status(t, s.post("/messages/"+s.id(w.MID("fix-me"))+"/correct", url.Values{"dest": {"Bills"}, "context": {"message"}}, hx), 200, "card")
	contains(t, card, "Correction recorded", "Corrected to Bills. The message was moved.", `role="status"`)
	lacks(t, card, "hx-swap-oob")
}

func TestUICorrectionWhenTheMailboxIsDownStillRecords(t *testing.T) {
	w := liveWorld(t)
	s := liveSite(t, w, func(context.Context, string) (mail.Client, error) { return nil, errors.New("imap unreachable") })
	id := w.MID("fix-me")
	body := status(t, s.post("/messages/"+s.id(id)+"/correct", url.Values{"dest": {"Bills"}, "context": {"row"}}, hx), 200, "down")
	contains(t, body, "Correction recorded", "It could not be moved yet: RuntimeError.")
	if got := s.column("SELECT folder FROM corrections WHERE message_id = ?", id); len(got) != 1 || got[0] != "Bills" {
		t.Errorf("corrections = %v", got)
	}
}

func TestPageFrameFollowsTheMailModePin(t *testing.T) {
	w := liveWorld(t)
	s := liveSite(t, w, nil)
	live := status(t, s.get("/triage?account=icloud"), 200, "live")
	contains(t, live, "Live", "/live/pause")
	lacks(t, live, "Shadow")

	mailtest.PinMode(t, "shadow")
	shadow := status(t, s.get("/triage?account=icloud"), 200, "shadow")
	contains(t, shadow, "Shadow")
	lacks(t, shadow, "/live/pause", "/live/resume")

	mailtest.PinMode(t, "live")
	again := status(t, s.get("/triage?account=icloud"), 200, "live again")
	contains(t, again, "/live/pause")
}
