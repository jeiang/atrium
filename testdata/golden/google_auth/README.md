# Google OAuth and Calendar request goldens

Generated from the frozen Python app; not regenerable (commit `cfd9dc1e9a5e653a48863c7e71370ba2ea1aac40`). Do not edit by hand; a golden changes only with the behavior it records; the provenance is the git history of testdata.

Each `<scenario>.json` holds `exchanges`: every HTTP request the Python code sent, intercepted where it leaves the
process (`urllib.request.urlopen` for the urllib clients, an `httpx.MockTransport` for the httpx clients,
`httplib2.Http.request` for the Google API client), and the scripted response it received. A request has `method`,
`url` (scheme, host and path), `path`, `query` (as sent), `headers` (lower-cased, only those the app sets:
transport-generated `host`, `content-length`, `connection`, `accept-encoding`, the httpx and httplib2 default
`user-agent`, a wildcard `accept` and googleapiclient's `x-goog-api-client` version tag are dropped; a multipart
`content-type` loses its random boundary), `body_encoding` and `body`: `json` (parsed), `form` (ordered
`[name, value]` pairs), `multipart` (parts with `name`, `filename`, `content_type` and `text` or `base64`), `text`,
`base64` or `none`. Responses
carry `status`, the `content-type`/`retry-after` headers and the body in the same encodings. Compare JSON bodies
with canonical-JSON equality (`internal/testutil/golden`), never byte equality.

Credentials are fixed fakes (client `golden-google-client-id`/`golden-google-client-secret`, refresh token `golden-google-refresh-token`, access token `golden-google-access-token`); hosts other than the real public APIs are `.invalid` or `.test`
placeholders. The time zone is `America/Port_of_Spain`.

Notes:
- Consent URL the CLI prints for the code_exchange scenario (not a request): `https://accounts.google.com/o/oauth2/v2/auth?client_id=golden-google-client-id&redirect_uri=http%3A%2F%2F127.0.0.1%3A8765&response_type=code&scope=https%3A%2F%2Fwww.googleapis.com%2Fauth%2Fcalendar.events&access_type=offline&prompt=consent&state=golden-token-urlsafe-16&code_challenge=0rnfdO8zLHbPYynUP5e9BxvYBwHL12yVVJTRDlfzAis&code_challenge_method=S256`.
- `secrets.token_urlsafe` is pinned to `golden-token-urlsafe-<n>` (verifier `golden-token-urlsafe-64`, state `golden-token-urlsafe-16`) and the loopback port to 8765, so the code exchange is reproducible.
- The Calendar client uses googleapiclient's bundled discovery document (`cache_discovery=False`, static discovery): no discovery request is sent.

`manifest.json` lists every scenario with its description and the Python tests it mirrors.

| Scenario | Requests | Mirrors |
| --- | --- | --- |
| `code_exchange` | 1 | `tests/test_calendar.py::test_google_auth_loopback_exchange` |
| `event_create` | 4 | `tests/test_calendar.py::test_create_google_sends_no_updates_no_attendees_and_logs_undo`, `tests/test_calendar.py::test_gmail_invite_targets_google` |
| `event_delete` | 2 | `tests/test_calendar.py::test_create_google_sends_no_updates_no_attendees_and_logs_undo` |
| `already_applied` | 6 | `src/atrium/calendar.py::create_event (409 branch; no dedicated test)`, `src/atrium/calendar.py::delete_event (404/410 branch; no dedicated test)` |
| `events_list` | 3 | `tests/test_calendar.py::test_list_merges_mirror_and_google` |
