package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
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
	setEnv(t, anthropic.BaseURLEnv, api.URL)
	if ran := runApp(t, "agent", "run", "--text", "please", "--kind", "chat"); ran.code != cli.ExitOK {
		t.Fatalf("agent run: %d %s", ran.code, ran.stderr)
	}
	requests := api.Requests()
	if len(requests) != 2 {
		t.Fatalf("%d model requests, want 2", len(requests))
	}
	d := mustConnect(t, filepath.Join(activeFixture.dir, "atrium.db"))
	var raw string
	if err := d.Read.QueryRow("SELECT content_json FROM thread_messages WHERE thread_id = (SELECT MAX(id) FROM threads) AND role = 'user' AND content_json LIKE '%tool_result%' ORDER BY seq DESC LIMIT 1").Scan(&raw); err != nil {
		t.Fatalf("tool result: %v", err)
	}
	var blocks []struct {
		Type    string `json:"type"`
		Content any    `json:"content"`
	}
	if err := json.Unmarshal([]byte(raw), &blocks); err != nil {
		t.Fatal(err)
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			continue
		}
		switch c := b.Content.(type) {
		case string:
			return c
		case []any:
			var out strings.Builder
			for _, part := range c {
				if p, ok := part.(map[string]any); ok {
					if text, ok := p["text"].(string); ok {
						out.WriteString(text)
					}
				}
			}
			return out.String()
		}
	}
	t.Fatalf("no tool_result block in %s", raw)
	return ""
}

func event(uid, start, end string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//test//EN\r\nBEGIN:VEVENT\r\nUID:" + uid + "\r\nDTSTAMP:20261001T000000Z\r\n" +
		start + "\r\n" + end + "\r\nSUMMARY:" + uid + "\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
}

func TestFindSlotHonoursOverlapsAllDayEventsWorkingHoursAndDST(t *testing.T) {
	t.Parallel()
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	setEnv(t, "ATRIUM_CORE_TZ", "America/New_York")
	setEnv(t, settingEnv(calendar.WorkDaysKey), "sun,mon")
	mirror := filepath.Join(dir, "calendars")
	setEnv(t, settingEnv(calendar.CalendarDirKey), mirror)
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
	want := `[{"end": "2026-11-01T10:00:00-05:00", "start": "2026-11-01T09:00:00-05:00"}, ` +
		`{"end": "2026-11-01T17:00:00-05:00", "start": "2026-11-01T12:00:00-05:00"}, ` +
		`{"end": "2026-11-02T13:00:00-05:00", "start": "2026-11-02T09:00:00-05:00"}, ` +
		`{"end": "2026-11-02T17:00:00-05:00", "start": "2026-11-02T13:30:00-05:00"}]`
	if got := unescape(toolResult(t, "find_slot", input)); !strings.Contains(got, want) {
		t.Errorf("find_slot result lacks the expected windows:\n%s", got)
	}

	setEnv(t, settingEnv(calendar.WorkHoursKey), "13:00-14:00")
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
	if err := json.Unmarshal([]byte(body), &req); err != nil || len(req.Messages) == 0 {
		return body
	}
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
	"DTSTART:20261110T170000Z\r\nDTEND:20261110T180000Z\r\nSUMMARY:Lunch\r\nLOCATION:Cafe\r\nDESCRIPTION:Lunch with Pat\r\nRRULE:FREQ=WEEKLY;COUNT=4\r\n" +
	"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\nORGANIZER:mailto:pat@example.com\r\n" +
	"ATTENDEE;PARTSTAT=ACCEPTED:mailto:pat@example.com\r\nATTENDEE;PARTSTAT=NEEDS-ACTION;RSVP=TRUE:mailto:operator@icloud.invalid\r\n" +
	"BEGIN:VALARM\r\nACTION:DISPLAY\r\nTRIGGER:-PT15M\r\nDESCRIPTION:Lunch\r\nEND:VALARM\r\n" +
	"END:VEVENT\r\nEND:VCALENDAR\r\n"

func proposalID(t *testing.T, result string) string {
	t.Helper()
	text := unescape(result)
	var parsed map[string]any
	if json.Unmarshal([]byte(text), &parsed) == nil {
		if id, ok := parsed["proposal_id"]; ok {
			return fmt.Sprint(id)
		}
	}
	_, rest, ok := strings.Cut(text, `"proposal_id": `)
	if !ok {
		t.Fatalf("no proposal in %s", text)
	}
	return strings.TrimRight(strings.FieldsFunc(rest, func(r rune) bool { return r == ',' || r == '}' })[0], " ")
}

func TestRSVPAndUpdateProposalsWriteOnlyOnApprovalAndUndo(t *testing.T) {
	t.Parallel()
	_, srv := davFixture(t)
	path := caldavtest.CalendarPath(0) + "lunch.ics"
	srv.SetObject(path, invite)

	rsvp := proposalID(t, toolResult(t, "calendar_rsvp", map[string]any{"calendar": "icloud", "uid": "lunch", "response": "accepted", "title": "Lunch"}))
	if len(srv.Puts) != 0 {
		t.Fatalf("proposing wrote to the calendar: %v", srv.Puts)
	}
	if got := runApp(t, "proposals", "approve", rsvp); got.code != cli.ExitOK {
		t.Fatalf("approve rsvp: %d %s", got.code, got.stderr)
	}
	body := srv.Objects()[path]
	if !strings.Contains(body, "PARTSTAT=ACCEPTED:mailto:operator@icloud.invalid") || !strings.Contains(body, "mailto:pat@example.com") {
		t.Fatalf("after the approved rsvp the event is %q", body)
	}
	if strings.Count(body, "ATTENDEE") != 2 {
		t.Errorf("the rsvp changed the attendee list: %q", body)
	}
	if got := runApp(t, "proposals", "approve", rsvp); got.code == cli.ExitOK || len(srv.Puts) != 1 {
		t.Errorf("a second approval wrote again: exit %d, %d puts", got.code, len(srv.Puts))
	}

	update := proposalID(t, toolResult(t, "calendar_update", map[string]any{
		"calendar": "icloud", "uid": "lunch", "title": "Long lunch", "start": "2026-11-10T12:00:00", "end": "2026-11-10T14:00:00",
	}))
	if got := runApp(t, "proposals", "approve", update); got.code != cli.ExitOK {
		t.Fatalf("approve update: %d %s", got.code, got.stderr)
	}
	body = srv.Objects()[path]
	if !strings.Contains(body, "SUMMARY:Long lunch") || strings.Count(body, "ATTENDEE") != 2 || !strings.Contains(body, "ORGANIZER") {
		t.Fatalf("after the approved update the event is %q", body)
	}
	for _, kept := range []string{"RRULE:FREQ=WEEKLY;COUNT=4", "BEGIN:VALARM", "X-APPLE-TRAVEL-ADVISORY-BEHAVIOR", "SEQUENCE:1"} {
		if !strings.Contains(body, kept) {
			t.Errorf("the update lost %s: %q", kept, body)
		}
	}
	for _, cleared := range []string{"LOCATION:Cafe", "DESCRIPTION:Lunch with Pat"} {
		if strings.Contains(body, cleared) {
			t.Errorf("the update did not clear %s: %q", cleared, body)
		}
	}
	if got := runApp(t, "proposals", "undo", update); got.code != cli.ExitOK {
		t.Fatalf("undo update: %d %s", got.code, got.stderr)
	}
	if body = srv.Objects()[path]; !strings.Contains(body, "SUMMARY:Lunch") || strings.Contains(body, "Long lunch") ||
		!strings.Contains(body, "LOCATION:Cafe") || !strings.Contains(body, "DESCRIPTION:Lunch with Pat") {
		t.Errorf("undo did not restore the event: %q", body)
	}
}

func TestCalendarListFiltersByTargetAndCollection(t *testing.T) {
	dir, _, _ := moduleFixture(t, flagsExcept("", ""))
	mirror := filepath.Join(dir, "calendars")
	setEnv(t, settingEnv(calendar.CalendarDirKey), mirror)
	if err := os.MkdirAll(filepath.Join(mirror, "home"), 0o750); err != nil {
		t.Fatal(err)
	}
	body := event("a", "DTSTART:20261110T170000Z", "DTEND:20261110T180000Z")
	if err := os.WriteFile(filepath.Join(mirror, "home", "a.ics"), []byte(body), 0o640); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"start": "2026-11-10T00:00:00", "end": "2026-11-11T00:00:00", "calendar": "icloud"}
	if got := unescape(toolResult(t, "calendar_list", input)); !strings.Contains(got, `"uid": "a"`) {
		t.Errorf("calendar_list(calendar=icloud) did not list the mirror: %s", got)
	}
	input["calendar"] = "home"
	if got := unescape(toolResult(t, "calendar_list", input)); !strings.Contains(got, `"uid": "a"`) {
		t.Errorf("calendar_list(calendar=home) did not list the collection: %s", got)
	}
	input["calendar"] = "google"
	if got := unescape(toolResult(t, "calendar_list", input)); strings.Contains(got, `"uid": "a"`) {
		t.Errorf("calendar_list(calendar=google) listed the iCloud mirror: %s", got)
	}
}

func TestCalendarUnknownWriteStaysBlocked(t *testing.T) {
	dir, srv := davFixture(t)
	path := caldavtest.CalendarPath(0) + "lunch.ics"
	srv.SetObject(path, invite)
	update := proposalID(t, toolResult(t, "calendar_update", map[string]any{
		"calendar": "icloud", "uid": "lunch", "title": "Long lunch", "start": "2026-11-10T12:00:00", "end": "2026-11-10T14:00:00",
	}))
	srv.FailPuts = 1
	srv.PutStatus = http.StatusInternalServerError
	if got := runApp(t, "proposals", "approve", update); got.code == cli.ExitOK {
		t.Fatalf("approve with a failing remote write exited 0: %q", got.stdout)
	}
	if srv.PutAttempts != 1 {
		t.Fatalf("%d PUT attempts, want 1", srv.PutAttempts)
	}
	if got := runApp(t, "proposals", "approve", update); got.code == cli.ExitOK || !strings.Contains(got.stderr, "resolve it in the mailbox") {
		t.Fatalf("second approve = %d %q, want an unresolved-write refusal", got.code, got.stderr)
	}
	if srv.PutAttempts != 1 {
		t.Fatalf("the blocked proposal retried the remote write: %d attempts", srv.PutAttempts)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var state string
	if err := d.Read.QueryRow("SELECT state FROM proposal_actions WHERE proposal_id = ? ORDER BY id DESC LIMIT 1", update).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "unknown" {
		t.Errorf("action state = %q, want unknown", state)
	}
}

func TestDirectCalendarUnknownWriteBlocksUntilResolved(t *testing.T) {
	dir, srv := davFixture(t)
	srv.FailPuts = 1
	srv.PutStatus = http.StatusInternalServerError
	input := map[string]any{"calendar": "icloud", "title": "Blocked", "start": "2026-11-10T12:00:00", "end": "2026-11-10T13:00:00"}
	first := unescape(toolResult(t, "calendar_create", input))
	if !strings.Contains(first, "500") {
		t.Fatalf("first create did not report the failed remote write: %s", first)
	}
	d := mustConnect(t, filepath.Join(dir, "atrium.db"))
	var id int64
	if err := d.Read.QueryRow("SELECT id FROM agent_writes WHERE state = 'unknown' AND tool = 'calendar_create' ORDER BY id DESC LIMIT 1").Scan(&id); err != nil {
		t.Fatalf("no unknown write: %v", err)
	}
	second := unescape(toolResult(t, "calendar_create", input))
	if !strings.Contains(second, "unknown outcome") || !strings.Contains(second, "writes resolve") {
		t.Fatalf("second create was not refused with the resolve hint: %s", second)
	}
	if srv.PutAttempts != 1 {
		t.Fatalf("the refused create reached the remote: %d attempts", srv.PutAttempts)
	}
	if got := runApp(t, "writes", "resolve", strconv.FormatInt(id, 10)); got.code != cli.ExitOK || !strings.Contains(got.stdout, `"resolved": true`) {
		t.Fatalf("writes resolve: %d %q %q", got.code, got.stdout, got.stderr)
	}
	third := unescape(toolResult(t, "calendar_create", input))
	if !strings.Contains(third, `"uid"`) {
		t.Fatalf("create after resolve failed: %s", third)
	}
	if srv.PutAttempts != 2 {
		t.Fatalf("%d PUT attempts after resolve, want 2", srv.PutAttempts)
	}
}
