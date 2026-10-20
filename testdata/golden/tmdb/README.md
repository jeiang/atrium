# TMDB request goldens

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

Credentials are fixed fakes (`api_key=k`); hosts other than the real public APIs are `.invalid` or `.test`
placeholders. The time zone is `America/Port_of_Spain`.

Notes:
- `TMDB_API_KEY` travels as the `api_key` query parameter; the goldens use the fake's key `k`.

`manifest.json` lists every scenario with its description and the Python tests it mirrors.

| Scenario | Requests | Mirrors |
| --- | --- | --- |
| `client_requests` | 9 | `tests/test_media.py::test_channels_page_search_preview_add_remove_poll`, `tests/test_media.py::test_library_pages_posters_and_thumbnails`, `src/atrium/media/tmdb.py::Tmdb (every method)` |
| `not_found` | 3 | `src/atrium/media/tmdb.py::Tmdb._get (404 branch)` |
