# CBTT request goldens

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

Credentials are fixed fakes (none: the CBTT homepage is public); hosts other than the real public APIs are `.invalid` or `.test`
placeholders. The time zone is `America/Port_of_Spain`.

`manifest.json` lists every scenario with its description and the Python tests it mirrors.

| Scenario | Requests | Mirrors |
| --- | --- | --- |
| `fx_rates_job` | 1 | `tests/test_money.py::test_fx_rates_builtin_job`, `tests/test_money.py::test_fetch_and_rate_lookup` |
| `first_run` | 1 | `tests/test_money.py::test_first_run_fetch_only_when_empty` |
