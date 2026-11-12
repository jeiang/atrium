package main

import (
	"context"
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jeiang/atrium/internal/cli"
	"github.com/jeiang/atrium/internal/config"
	"github.com/jeiang/atrium/internal/db"
	"github.com/jeiang/atrium/internal/inference"
	"github.com/jeiang/atrium/internal/mail/imapclient"
	"github.com/jeiang/atrium/internal/settings"
)

type recoveryClock struct{ at float64 }

func (c *recoveryClock) now() float64 { return c.at }

func recoveryApp(f *fixture, now func() float64) *cli.App {
	reg := settings.New()
	return newAppWith(f.lookup, reg, config.NewState(reg), imapclient.Opener, now, quietLog)
}

func recoveryRun(t *testing.T, f *fixture, clock *recoveryClock, text string) (asstView, appRun) {
	t.Helper()
	ran := asstRunApp(recoveryApp(f, clock.now), context.Background(), "agent", "run", "--text", text)
	var v asstView
	if err := json.Unmarshal([]byte(ran.stdout), &v); err != nil {
		return v, ran
	}
	return v, ran
}

func recoveryRow(t *testing.T, d *db.DB, runID int64) (state string, reservation, accounted int64) {
	t.Helper()
	if err := d.Read.QueryRow("SELECT state, reservation_nusd, accounted_nusd FROM usage WHERE run_id = ?", runID).
		Scan(&state, &reservation, &accounted); err != nil {
		t.Fatalf("usage row of run %d: %v", runID, err)
	}
	return
}

func recoveryReservation(t *testing.T, d *db.DB, runID int64) int64 {
	t.Helper()
	_, reservation, _ := recoveryRow(t, d, runID)
	return reservation
}

func recoveryMonthCommitted(t *testing.T, d *db.DB, month string) int64 {
	t.Helper()
	var n int64
	if err := d.Read.QueryRow(
		"SELECT COALESCE(SUM(CASE WHEN budget_class = 'subscription' THEN 0 WHEN state IN ('reserved','unknown') THEN reservation_nusd ELSE accounted_nusd END), 0) "+
			"FROM usage WHERE month = ?", month).Scan(&n); err != nil {
		t.Fatalf("committed amount of month %v: %v", month, err)
	}
	return n
}

func recoveryBudget(ceilingN int64) string {
	return strconv.FormatFloat(float64(ceilingN)/inference.NanoUSD/(1-inference.ClassificationReserveShare), 'f', 9, 64)
}

func recoveryRefused(t *testing.T, ran appRun) {
	t.Helper()
	if !strings.Contains(ran.stderr+ran.stdout, "monthly budget") {
		t.Fatalf("run: exit %d stdout %q stderr %q, want a monthly budget refusal", ran.code, ran.stdout, ran.stderr)
	}
}

func recoveryStatus(t *testing.T, d *db.DB, at float64, loc *time.Location) inference.Status {
	t.Helper()
	ctx := settings.With(context.Background(), settings.New())
	status, err := inference.StatusAt(ctx, d.Read, at, loc)
	if err != nil {
		t.Fatalf("status at %v: %v", at, err)
	}
	return status
}

func TestInferenceUnknownReservationSurvivesRestartAndExpiresToItsEstimate(t *testing.T) {
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	clock := &recoveryClock{at: float64(time.Date(2026, 3, 10, 12, 0, 0, 0, loc).Unix())}
	dir, api, f := moduleFixtureAt(t, flagsExcept("", ""), clock.now)
	d := asstDB(t, dir)

	api.Push(asstReply("warm"))
	warm, ran := recoveryRun(t, f, clock, "warm")
	if ran.code != cli.ExitOK {
		t.Fatalf("warm run: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	estimate := recoveryReservation(t, d, warm.ID)

	api.Disconnect.Store(true)
	lost, ran := recoveryRun(t, f, clock, "lost")
	if lost.State != "failed" {
		t.Fatalf("disconnected run: %+v %q", lost, ran.stderr)
	}
	state, reservation, accounted := recoveryRow(t, d, lost.ID)
	if state != "unknown" || reservation <= 0 || accounted != 0 {
		t.Fatalf("unknown row: state %q reservation %d accounted %d", state, reservation, accounted)
	}

	api.Disconnect.Store(false)
	api.Push(asstReply("again"))
	if _, ran := recoveryRun(t, f, clock, "warm"); ran.code != cli.ExitOK {
		t.Fatalf("run after restart: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	if state, reservation, accounted := recoveryRow(t, d, lost.ID); state != "unknown" || reservation <= 0 || accounted != 0 {
		t.Fatalf("row after restart: state %q reservation %d accounted %d, want the reservation kept", state, reservation, accounted)
	}

	clock.at += (inference.UnknownExpiry + time.Hour).Seconds()
	month := inference.MonthKey(clock.at, loc)
	current := recoveryMonthCommitted(t, d, month)
	asstSet(t, inference.BudgetKey, recoveryBudget(current+estimate/2))
	before := len(api.Requests())
	_, refused := recoveryRun(t, f, clock, "warm")
	recoveryRefused(t, refused)
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the expired estimate freed budget: %d provider requests", got)
	}
	state, reservation, accounted = recoveryRow(t, d, lost.ID)
	if state != "expired" || accounted != reservation || reservation <= 0 {
		t.Fatalf("expired row after a refused reservation: state %q reservation %d accounted %d", state, reservation, accounted)
	}
	status := recoveryStatus(t, d, clock.at, loc)
	if status.CommittedUSD != usdOfNano(current) {
		t.Errorf("refused reservation freed budget: committed %v, want %v", status.CommittedUSD, usdOfNano(current))
	}
	if remaining := status.BudgetUSD - status.CommittedUSD; math.Abs(status.RemainingUSD-remaining) > 1e-9 {
		t.Errorf("refused reservation freed budget: remaining %v, want %v", status.RemainingUSD, remaining)
	}
}

func TestInferenceReservationAtTheMonthBoundaryCarriesIntoTheNextMonth(t *testing.T) {
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	clock := &recoveryClock{at: float64(time.Date(2026, 1, 31, 23, 59, 0, 0, loc).Unix())}
	dir, api, f := moduleFixtureAt(t, flagsExcept("", ""), clock.now)
	d := asstDB(t, dir)

	api.Disconnect.Store(true)
	lost, ran := recoveryRun(t, f, clock, "lost")
	if lost.State != "failed" {
		t.Fatalf("disconnected run: %+v %q", lost, ran.stderr)
	}
	january := inference.MonthKey(clock.at, loc)
	var month string
	if err := d.Read.QueryRow("SELECT month FROM usage WHERE run_id = ?", lost.ID).Scan(&month); err != nil {
		t.Fatal(err)
	}
	state, reservation, _ := recoveryRow(t, d, lost.ID)
	if month != january || state != "unknown" || reservation <= 0 {
		t.Fatalf("row admitted at 23:59: month %v state %q reservation %d, want month %v and unknown", month, state, reservation, january)
	}

	clock.at = float64(time.Date(2026, 2, 1, 0, 0, 30, 0, loc).Unix())
	february := inference.MonthKey(clock.at, loc)
	if february == january {
		t.Fatalf("the clock did not cross the month boundary")
	}
	status := recoveryStatus(t, d, clock.at, loc)
	if status.CarryInUSD != usdOfNano(reservation) || status.CommittedUSD != 0 {
		t.Fatalf("new month status: carry-in %v committed %v, want carry-in %v and no committed spend",
			status.CarryInUSD, status.CommittedUSD, usdOfNano(reservation))
	}

	api.Disconnect.Store(false)
	api.Push(asstReply("warm"))
	warm, ran := recoveryRun(t, f, clock, "warm")
	if ran.code != cli.ExitOK {
		t.Fatalf("warm run in the new month: exit %d stdout %q stderr %q", ran.code, ran.stdout, ran.stderr)
	}
	estimate := recoveryReservation(t, d, warm.ID)
	committed := recoveryMonthCommitted(t, d, february)
	status = recoveryStatus(t, d, clock.at, loc)
	if status.CarryInUSD != usdOfNano(reservation) || status.CommittedUSD != usdOfNano(committed) {
		t.Fatalf("status after the new month's run: carry-in %v committed %v, want carry-in %v and committed %v",
			status.CarryInUSD, status.CommittedUSD, usdOfNano(reservation), usdOfNano(committed))
	}

	ceiling := committed + estimate + reservation/2
	asstSet(t, inference.BudgetKey, recoveryBudget(ceiling))
	before := len(api.Requests())
	_, refused := recoveryRun(t, f, clock, "warm")
	recoveryRefused(t, refused)
	if got := len(api.Requests()) - before; got != 0 {
		t.Errorf("the carry-in freed budget: %d provider requests", got)
	}
	if state, _, _ := recoveryRow(t, d, lost.ID); state != "unknown" {
		t.Errorf("the carried reservation is %q, want unknown", state)
	}
}

func TestInferenceStatusReportsEffectiveExpiryWithoutWriting(t *testing.T) {
	loc, err := time.LoadLocation("America/Port_of_Spain")
	if err != nil {
		t.Fatal(err)
	}
	clock := &recoveryClock{at: float64(time.Date(2026, 3, 10, 12, 0, 0, 0, loc).Unix())}
	dir, api, f := moduleFixtureAt(t, flagsExcept("", ""), clock.now)
	d := asstDB(t, dir)

	api.Disconnect.Store(true)
	lost, ran := recoveryRun(t, f, clock, "lost")
	if lost.State != "failed" {
		t.Fatalf("disconnected run: %+v %q", lost, ran.stderr)
	}
	_, reservation, _ := recoveryRow(t, d, lost.ID)
	status := recoveryStatus(t, d, clock.at, loc)
	if status.UnknownUSD != usdOfNano(reservation) || status.ExpiredUSD != 0 {
		t.Fatalf("status before expiry: unknown %v expired %v, want unknown %v", status.UnknownUSD, status.ExpiredUSD, usdOfNano(reservation))
	}

	clock.at += (inference.UnknownExpiry + time.Hour).Seconds()
	status = recoveryStatus(t, d, clock.at, loc)
	if status.ExpiredUSD != usdOfNano(reservation) {
		t.Errorf("effective expiry: expired estimate %v, want %v", status.ExpiredUSD, usdOfNano(reservation))
	}
	if status.CommittedUSD != usdOfNano(reservation) {
		t.Errorf("effective expiry: committed %v, want the estimate %v still counted", status.CommittedUSD, usdOfNano(reservation))
	}
	if status.UnknownUSD != 0 || status.ReservedUSD != 0 {
		t.Errorf("effective expiry: unknown %v reserved %v, want both zero", status.UnknownUSD, status.ReservedUSD)
	}
	if remaining := status.BudgetUSD - usdOfNano(reservation); math.Abs(status.RemainingUSD-remaining) > 1e-9 {
		t.Errorf("effective expiry: remaining %v, want %v", status.RemainingUSD, remaining)
	}

	state, keptReservation, accounted := recoveryRow(t, d, lost.ID)
	if state != "unknown" || keptReservation != reservation || accounted != 0 {
		t.Errorf("read-only status wrote the row: state %q reservation %d accounted %d", state, keptReservation, accounted)
	}
}

func usdOfNano(n int64) float64 { return float64(n) / inference.NanoUSD }
