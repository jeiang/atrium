# App event stream

`GET /api/v1/events` (bearer) and `GET /web/v1/events` (web session, the same binding) tell a client which read models changed, so it can refetch them. The stream carries topic names and revision numbers only, never content.

## Frames

A 200 `text/event-stream` starts with `retry: 1000`, then one `sync` event, then a `change` event each time a topic's revision moves. `docs/api/openapi.json` types every frame as `AppEvent`: the `event` name and the `data` line, a JSON text (`contentMediaType: application/json`) whose decoded value the event's `contentSchema` describes. Frames carry no `id:`; there is no cursor and no replay.

| Event | `data` |
|---|---|
| `sync` | `topics`: `[{topic,revision}]`, the current revision of every topic the caller may see |
| `change` | `{topic,revision}`: one topic whose revision differs from the last one sent on this stream |

A revision is a counter the database advances on every write to a table of that topic. Compare revisions for equality only: a restored database can move a counter back. After a reconnect the new `sync` replaces everything the client knew; refetch each topic whose revision differs from the one it holds. Clients ignore event names, topics and data fields they do not know; new ones are added within v1.

## Topics

| Topic | Sent while |
|---|---|
| `review`, `effects`, `jobs`, `settings`, `connections`, `attention`, `ops`, `auth` | always |
| `assistant`, `knowledge`, `tasks` | the assistant module is on |
| `mail` | the mail module is on |
| `calendar` | the calendar module is on |
| `media` | the media module is on |
| `notify` | the notifications module is on |
| `downloads` | the torrents module is on; it also advances when qBittorrent reports a torrent's name, state, tags or whole-percent progress changed (checked every 5 seconds) |

A topic whose module turns off stops being sent; when the module turns on again, its next check sends a `change` for it. Lease, heartbeat and streamed run event writes do not advance a revision; follow a run with its run stream (`run-stream.md`).

## Delivery and limits

A write committed through the server's transaction path wakes every open stream at once. Writes from other processes, such as CLI commands, and single-statement writes outside a transaction are found by a check every 2 seconds. `: heartbeat` is sent every 15 seconds; each heartbeat re-checks the bearer token or browser session and closes the stream when it is no longer valid.

Open app event streams are limited to 2 per credential and 8 per server; a refused stream gets 429 `rate_limited`. Run streams have their own limit (4 per credential, 16 per server) and do not count against this one. The credential is the bearer token on `/api/v1`, and on `/web/v1` the session on OIDC sign-in, or the browser (its CSRF cookie) under proxy or local authentication. `/web/v1` reads need the `Atrium-Client: web` header, which `EventSource` cannot send, so a browser client reads the stream with `fetch`. The route answers no `ETag` and never answers 304.
