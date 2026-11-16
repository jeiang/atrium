# Run event stream

`GET /api/v1/threads/{thread_id}/runs/{run_id}/events` (bearer), `GET /web/v1/threads/{thread_id}/runs/{run_id}/events` (web session, the same binding) and `GET /chat/{thread_id}/events?run={run_id}` (session) read the same durable stream with the same encoder.

## Cursor

Each run has its own stream. `id:` is the decimal sequence number in that stream, starting at 1. Events of descendant runs are copied into every ancestor stream in commit order; `data.run_id` names the source run. The cursor is the last received id, sent as `Last-Event-ID` or as the `after` query parameter. Both follow one rule: a decimal integer; `Last-Event-ID` wins when both are present. Without a cursor the stream starts at 0.

| Request | Answer |
|---|---|
| unknown run, or a run of another thread | 404 `not_found` |
| malformed cursor (`after` or `Last-Event-ID`), or a cursor after the last written event | 400 `bad_request` |
| cursor before the retained prefix | 410 `stream_expired`; the body is the standard error envelope plus a top-level `run_id` (`{"error":{…},"run_id":N}`), because the envelope has no details field |
| cursor at or after the terminal event | 204 |
| otherwise | 200 `text/event-stream` |

A 200 starts with `retry: 1000`, then replays every event after the cursor in order and follows new events. `: heartbeat` is sent after 15 seconds without traffic; each heartbeat re-checks the bearer token or browser session and closes the stream when it is no longer valid. Open run streams are limited to 4 per credential and 16 per server (429 `rate_limited`); app event streams (`app-events.md`) have their own limit and do not count against it. The credential is the bearer token on `/api/v1`, and on `/web/v1` and the pages the session on OIDC sign-in, and the browser (its CSRF cookie) under proxy or local authentication, which carry no per-session identity. The route answers no `ETag`, ignores `If-None-Match` and never answers 304.

## Events

`docs/api/openapi.json` types every frame as `RunStreamEvent`: the `event` name, the `id` cursor and the JSON `data`. Every durable event's `data` carries `run_id`, the run that wrote it. Clients ignore event names and data fields they do not know; new ones are added within v1.

| Event | `data` besides `run_id` |
|---|---|
| `run_start` | `thread_id`, `thread_kind`, `role` (string or null), `title` (string or null), `effort`, `parent_run_id` (integer or null) |
| `thinking`, `text` | `text`: the next chunk of raw markdown |
| `tool_start` | `id`, `name`, `args` (the clipped arguments) |
| `tool_progress` | `id`, `text`: what the running tool call `id` is doing now; live only, see below |
| `tool_done` | `id`, `ok`, `preview`, optional `results`: `[{url,title}]` |
| `user` | `text`, `queue_id`: a queued message the run took up |
| `plan` | `items`: `[{text,done}]`, the whole plan |
| `run_done` | `state`, `error` (string or null); written once when a run ends |

Text is markdown: render CommonMark with tables, strikethrough and autolinks, and escape raw HTML. The same policy applies to stored message text. Link only `http`, `https` and `mailto` URLs and paths on the same origin, and show any other link as its text. Do not load images: show an image as a link with its alt text, so that a reply never makes a request to another site.

`tool_progress` is sent only for a tool that runs a sub-agent (`search_web`, `research`, `lookup`): each tool the sub-agent starts sends one event for the calling run, whose `text` is the tool name and its clipped arguments, as in the sub-agent's own `tool_start`. Other tools send none. It is live only: it has no `id:`, is never stored or replayed, does not move the cursor, and reaches only streams open in the process that runs the tool while it runs. The next `tool_progress` or the `tool_done` of the same `id` supersedes it. The sub-agent's own durable events are copied into the stream as well, under its `run_id`.

`results` is the list the transcript shows on the finished step, so a client can draw sources without refetching: on the `tool_done` of a `web_search` call, written as soon as the search returns and before the run's answer, the search results (possibly empty); on a successful `search_web`, its verified sources. Other tool calls have no `results`. A missing title is the URL. Titles and URLs come from the web: treat them as untrusted text and link only `http` and `https` URLs.

Terminal events are durable and written exactly once, only to the run's own stream:

- `complete`: `{state:"done",thread_id}`
- `run_error`: `{state:"failed",problem:{code,message},effect_ids:[]}`

After the terminal event the server sends the non-durable compatibility event `done` `{state}` and closes. A parked run is not finished: when a stream is caught up with a parked run the server sends `done` `{state:"parked"}` and closes; reconnecting with the last id continues the same stream after the operator answers.

## Retention

Streams of active and parked runs are kept. A finished stream is kept for 30 days after its terminal event; then its events are removed and older cursors answer 410. Deleting a thread deletes its streams.
