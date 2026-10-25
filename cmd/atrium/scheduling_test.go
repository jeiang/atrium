package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeiang/atrium/internal/anthropic"
	"github.com/jeiang/atrium/internal/anthropic/anthropictest"
	"github.com/jeiang/atrium/internal/calendar"
	"github.com/jeiang/atrium/internal/calendar/caldavtest"
	"github.com/jeiang/atrium/internal/cli"
)

func toolResult(t *testing.T, name string, input map[string]any) string {
	t.Helper()
	api := &modelLog{Server: anthropictest.New(t,
		anthropictest.Reply("tool_use", anthropictest.ToolUse("c1", name, input)),
		anthropictest.Reply("end_turn", anthropictest.Text("done")),
	)}
	api.OnRequest = func(r anthropictest.Request) {
		api.mu.Lock()
		defer api.mu.Unlock()
		api.seen = append(api.seen, r)
	}
	t.Setenv(anthropic.BaseURLEnv, api.URL)
	if ran := runApp("agent", "run", "--text", "please", "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	return string(requests[1].Body)
}

func event(uid, start, end string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261001T000000Z\r\n" +
		start + "\r\n" + end + "\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestFindSlotHonoursOverlapsAllDayEventsWorkingHoursAndDST(t *testing.T) {
	dir, _ := moduleFixture(t, flagsExcept("", ""))
	t.Setenv("ATRIUM_CORE_TZ", "America/New_York")
	t.Setenv(settingEnv(calendar.WorkDaysKey), "sun,mon")
	mirror := filepath.Join(dir, "calendars")
	t.Setenv(settingEnv(calendar.CalendarDirKey), mirror)
	if err := os.MkdirAll(filepath.Join(mirror, "home"), 0o750); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"a": event("a", "DTSTART:20261101T150000Z", "DTEND:20261101T160000Z"),
		"b": event("b", "DTSTART:20261101T153000Z", "DTEND:20261101T170000Z"),
		"c": event("c", "DTSTART;VALUE=DATE:20261102", "DTEND;VALUE=DATE:20261103"),
		"d": event("d", "DTSTART:20261102T180000Z", "DTEND:20261102T183000Z"),
	} {
		if err := os.WriteFile(filepath.Join(mirror, "home", name+".ics"), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	input := map[string]any{"start": "2026-11-01T00:00:00", "end": "2026-11-03T00:00:00", "duration_minutes": 60}
	want := `[{"start": "2026-11-01T09:00:00-05:00", "end": "2026-11-01T10:00:00-05:00"}, ` +
		`{"start": "2026-11-01T12:00:00-05:00", "end": "2026-11-01T17:00:00-05:00"}, ` +
		`{"start": "2026-11-02T09:00:00-05:00", "end": "2026-11-02T13:00:00-05:00"}, ` +
		`{"start": "2026-11-02T13:30:00-05:00", "end": "2026-11-02T17:00:00-05:00"}]`
	if got := unescape(toolResult(t, "find_slot", input)); !strings.Contains(got, want) {
		t.Errorf("find_slot result lacks the expected windows:\n%s", got)
	}

	t.Setenv(settingEnv(calendar.WorkHoursKey), "13:00-14:00")
	if got := unescape(toolResult(t, "find_slot", input)); !strings.Contains(got, `"2026-11-01T13:00:00-05:00"`) || strings.Contains(got, `"2026-11-01T09:00:00-05:00"`) {
		t.Errorf("changing work_hours did not change the windows:\n%s", got)
	}
	input["duration_minutes"] = 1440
	if got := unescape(toolResult(t, "find_slot", input)); !strings.Contains(got, `"windows": []`) {
		t.Errorf("a duration longer than any gap should return no windows:\n%s", got)
	}
}

func unescape(body string) string {
	var req struct {
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	_ = json.Unmarshal([]byte(body), &req)
	var out strings.Builder
	for _, m := range req.Messages {
		var blocks []struct {
			Content any `json:"content"`
		}
		if json.Unmarshal(m.Content, &blocks) == nil {
			for _, b := range blocks {
				switch c := b.Content.(type) {
				case string:
					out.WriteString(c)
				case []any:
					for _, part := range c {
						if p, ok := part.(map[string]any); ok {
							if text, ok := p["text"].(string); ok {
								out.WriteString(text)
							}
						}
					}
				}
			}
		}
	}
	return out.String()
}

const invite = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:lunch\r\nDTSTAMP:20261001T000000Z\r\n" +
	"DTSTART:20261110T170000Z\r\nDTEND:20261110T180000Z\r\nSUMMARY:Lunch\r\nORGANIZER:mailto:pat@example.com\r\n" +
	"ATTENDEE;PARTSTAT=ACCEPTED:mailto:pat@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:operator@icloud.invalid\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func proposalID(t *testing.T, result string) string {
	t.Helper()
	text := unescape(result)
	_, rest, ok := strings.Cut(text, `"proposal_id": `)
	if !ok {
		t.Fatalf("no proposal in %s", text)
	}
	return strings.TrimRight(strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == '}' })[0], " ")
}

func TestRSVPAndUpdateProposalsWriteOnlyOnApprovalAndUndo(t *testing.T) {
	_, srv := davFixture(t)
	path := caldavtest.CalendarPath(0) + "lunch.ics"
	srv.SetObject(path, invite)

	rsvp := proposalID(t, toolResult(t, "calendar_rsvp", map[string]any{"calendar": "icloud", "uid": "lunch", "response": "accepted", "title": "Lunch"}))
	if len(srv.Puts) != 0 {
		t.Fatalf("proposing wrote to the calendar: %v", srv.Puts)
	}
	if got := runApp("proposals", "approve", rsvp); got.code != cli.ExitOK {
		t.Fatalf("approve rsvp: %d %s", got.code, got.stderr)
	}
	body := srv.Objects()[path]
	if !strings.Contains(body, "PARTSTAT=ACCEPTED:mailto:operator@icloud.invalid") || !strings.Contains(body, "mailto:pat@example.com") {
		t.Fatalf("after the approved rsvp the event is %q", body)
	}
	if strings.Count(body, "ATTENDEE") != 2 {
		t.Errorf("the rsvp changed the attendee list: %q", body)
	}
	if got := runApp("proposals", "approve", rsvp); got.code == cli.ExitOK || len(srv.Puts) != 1 {
		t.Errorf("a second approval wrote again: exit %d, %d puts", got.code, len(srv.Puts))
	}

	update := proposalID(t, toolResult(t, "calendar_update", map[string]any{
		"calendar": "icloud", "uid": "lunch", "title": "Long lunch", "start": "2026-11-10T12:00:00", "end": "2026-11-10T14:00:00",
	}))
	if got := runApp("proposals", "approve", update); got.code != cli.ExitOK {
		t.Fatalf("approve update: %d %s", got.code, got.stderr)
	}
	body = srv.Objects()[path]
	if !strings.Contains(body, "SUMMARY:Long lunch") || strings.Count(body, "ATTENDEE") != 2 || !strings.Contains(body, "ORGANIZER") {
		t.Fatalf("after the approved update the event is %q", body)
	}
	if got := runApp("proposals", "undo", update); got.code != cli.ExitOK {
		t.Fatalf("undo update: %d %s", got.code, got.stderr)
	}
	if body = srv.Objects()[path]; !strings.Contains(body, "SUMMARY:Lunch") || strings.Contains(body, "Long lunch") {
		t.Errorf("undo did not restore the event: %q", body)
	}
}
