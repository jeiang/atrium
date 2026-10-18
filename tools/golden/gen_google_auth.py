"""Google OAuth and Calendar request goldens: the installed-app code exchange (google_auth.authorize), the
refresh-token grant google-auth sends before each Calendar call, and every Calendar API request calendar.py issues
(insert, delete, list), answered by a scripted Google. Output: testdata/golden/google_auth/ (or argv[1])."""

import base64
import hashlib
import io
import json
import sys
from pathlib import Path
from types import SimpleNamespace
from zoneinfo import ZoneInfo

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import TZ, reply, write_slice  # noqa: E402

import httpx  # noqa: E402

from atrium import calendar, google_auth  # noqa: E402

SLICE = "google_auth"
API = "https://www.googleapis.com"
CLIENT_ID = "golden-google-client-id"
CLIENT_SECRET = "golden-google-client-secret"
REFRESH_TOKEN = "golden-google-refresh-token"
ACCESS_TOKEN = "golden-google-access-token"
PORT = 8765
CODE = "golden-google-code"
ENV = {"GOOGLE_OAUTH_CLIENT_ID": CLIENT_ID, "GOOGLE_OAUTH_CLIENT_SECRET": CLIENT_SECRET, "GOOGLE_REFRESH_TOKEN": REFRESH_TOKEN, "ATRIUM_TZ": TZ}
ZONE = ZoneInfo(TZ)
EVENTS = "/calendar/v3/calendars/primary/events"


def token_urlsafe(n):
    return f"golden-token-urlsafe-{n}"


VERIFIER, STATE = token_urlsafe(64), token_urlsafe(16)
CHALLENGE = base64.urlsafe_b64encode(hashlib.sha256(VERIFIER.encode()).digest()).rstrip(b"=").decode()


def token_endpoint(method, url, headers, body):
    form = dict(pair.split("=", 1) for pair in body.decode().split("&"))
    if form["grant_type"] == "authorization_code":
        return reply(200, {"access_token": ACCESS_TOKEN, "expires_in": 3599, "refresh_token": REFRESH_TOKEN, "scope": calendar.SCOPE, "token_type": "Bearer"})
    return reply(200, {"access_token": ACCESS_TOKEN, "expires_in": 3599, "scope": calendar.SCOPE, "token_type": "Bearer"})


def calendar_api(answers):
    queue = list(answers)

    def handler(method, url, headers, body):
        status, payload = queue.pop(0)
        return reply(status, payload) if payload is not None else (status, {}, b"")

    return handler, queue


def served(wire, *answers):
    wire.serve(calendar.TOKEN_URI, token_endpoint)
    handler, queue = calendar_api(answers)
    wire.serve(API, handler)
    return queue


class Loopback:
    """Stands in for the loopback HTTPServer: its one request is the browser redirect carrying code and state."""

    def __init__(self, address, handler):
        self.server_address = (address[0], address[1])
        self.handler = handler

    def handle_request(self):
        catch = object.__new__(self.handler)
        catch.path = f"/?state={STATE}&code={CODE}&scope={calendar.SCOPE}"
        catch.wfile = io.BytesIO()
        catch.send_response = catch.send_header = lambda *a: None
        catch.end_headers = lambda: None
        catch.do_GET()

    def server_close(self):
        pass


def code_exchange(wire, tmp):
    wire.serve(calendar.TOKEN_URI, token_endpoint)
    saved = google_auth.http.server.HTTPServer, google_auth.secrets
    google_auth.http.server.HTTPServer, google_auth.secrets = Loopback, SimpleNamespace(token_urlsafe=token_urlsafe)
    printed = []

    def post(url, data, timeout):
        with httpx.Client(transport=wire.transport, timeout=timeout) as client:
            return client.post(url, data=data)

    try:
        assert google_auth.authorize(CLIENT_ID, CLIENT_SECRET, port=PORT, out=printed.append, timeout=1, post=post) == REFRESH_TOKEN
    finally:
        google_auth.http.server.HTTPServer, google_auth.secrets = saved
    assert google_auth.auth_url(CLIENT_ID, f"http://127.0.0.1:{PORT}", STATE, CHALLENGE) in printed[0]


def spec(raw, uid):
    return calendar.normalize_spec(raw, ZONE, uid=uid)


def event_create(wire, tmp):
    queue = served(wire, (200, {"id": "dentist1", "status": "confirmed"}), (200, {"id": "holiday1", "status": "confirmed"}))
    calendar.create_event(ENV, calendar.GOOGLE, spec({"title": "Dentist", "start": "2026-10-20T13:00", "location": "Main St", "notes": "bring card"}, "dentist1"), ZONE)
    calendar.create_event(ENV, calendar.GOOGLE, spec({"title": "Holiday", "start": "2026-10-30", "all_day": True}, "holiday1"), ZONE)
    assert not queue


def event_delete(wire, tmp):
    queue = served(wire, (204, None))
    calendar.delete_event(ENV, calendar.GOOGLE, "dentist1")
    assert not queue


def already_applied(wire, tmp):
    error = {"error": {"code": 409, "message": "The requested identifier already exists."}}
    queue = served(wire, (409, error), (404, {"error": {"code": 404, "message": "Not Found"}}), (410, {"error": {"code": 410, "message": "Resource has been deleted"}}))
    calendar.create_event(ENV, calendar.GOOGLE, spec({"title": "Dentist", "start": "2026-10-20T13:00"}, "dentist1"), ZONE)
    calendar.delete_event(ENV, calendar.GOOGLE, "dentist1")
    calendar.delete_event(ENV, calendar.GOOGLE, "dentist1")
    assert not queue


def events_list(wire, tmp):
    first = {"items": [{"id": "e1", "status": "confirmed", "summary": "Standup", "start": {"dateTime": "2026-10-20T13:00:00Z"}, "end": {"dateTime": "2026-10-20T13:30:00Z"}}], "nextPageToken": "golden-page-2"}
    second = {"items": [{"id": "e2", "status": "cancelled"}, {"id": "e3", "summary": "Holiday", "start": {"date": "2026-10-30"}, "end": {"date": "2026-10-31"}, "location": "Home"}]}
    queue = served(wire, (200, first), (200, second))
    found = calendar.list_events(ENV, "2026-10-20", "2026-10-27", calendar.GOOGLE)
    assert [e["uid"] for e in found["events"]] == ["e1", "e3"]
    assert not queue


SCENARIOS = [
    ("code_exchange", "google_auth.authorize (`atrium calendar google-auth --port 8765`): after the loopback redirect, the authorization_code grant with the PKCE verifier and the loopback redirect_uri.", ["tests/test_calendar.py::test_google_auth_loopback_exchange"], code_exchange),
    ("event_create", "calendar.create_event on Google, timed (location and notes) and all-day: each call builds a fresh service, so google-auth first sends the refresh_token grant, then events.insert with the client-chosen id and sendUpdates=none.", ["tests/test_calendar.py::test_create_google_sends_no_updates_no_attendees_and_logs_undo", "tests/test_calendar.py::test_gmail_invite_targets_google"], event_create),
    ("event_delete", "calendar.delete_event on Google (undo): refresh grant, then events.delete with sendUpdates=none.", ["tests/test_calendar.py::test_create_google_sends_no_updates_no_attendees_and_logs_undo"], event_delete),
    ("already_applied", "An insert answered 409 (id already inserted) and deletes answered 404 and 410 are treated as done.", ["src/atrium/calendar.py::create_event (409 branch; no dedicated test)", "src/atrium/calendar.py::delete_event (404/410 branch; no dedicated test)"], already_applied),
    ("events_list", "calendar.list_events for Google: refresh grant, then events.list (singleEvents, orderBy startTime, maxResults 250) following nextPageToken.", ["tests/test_calendar.py::test_list_merges_mirror_and_google"], events_list),
]

NOTES = [
    f"Consent URL the CLI prints for the code_exchange scenario (not a request): `{google_auth.auth_url(CLIENT_ID, f'http://127.0.0.1:{PORT}', STATE, CHALLENGE)}`.",
    f"`secrets.token_urlsafe` is pinned to `golden-token-urlsafe-<n>` (verifier `{VERIFIER}`, state `{STATE}`) and the loopback port to {PORT}, so the code exchange is reproducible.",
    "The Calendar client uses googleapiclient's bundled discovery document (`cache_discovery=False`, static discovery): no discovery request is sent.",
]


if __name__ == "__main__":
    write_slice(SLICE, "Google OAuth and Calendar", __file__, SCENARIOS, f"client `{CLIENT_ID}`/`{CLIENT_SECRET}`, refresh token `{REFRESH_TOKEN}`, access token `{ACCESS_TOKEN}`", NOTES)
