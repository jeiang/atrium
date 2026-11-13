# Run event stream

`GET /api/v1/threads/{thread_id}/runs/{run_id}/events` (bearer) and `GET /chat/{thread_id}/events?run={run_id}` (session) read the same durable stream with the same encoder.

## Cursor

Each run has its own stream. `id:` is the decimal sequence number in that stream, starting at 1. Events of descendant runs are copied into every ancestor stream in commit order; `data.run_id` names the source run. Send the last received id as `Last-Event-ID`; it wins over the `after` query parameter. Without a cursor the stream starts at 0.

| Request | Answer |
|---|---|
| unknown run, or a run of another thread | 404 `not_found` |
| malformed cursor, or a cursor after the last written event | 400 `bad_request` |
| cursor before the retained prefix | 410 `stream_expired`, `error.run_id` set |
| cursor at or after the terminal event | 204 |
| otherwise | 200 `text/event-stream` |

A 200 starts with `retry: 1000`, then replays every event after the cursor in order and follows new events. `: heartbeat` is sent after 15 seconds without traffic; each heartbeat re-checks the bearer token or browser session and closes the stream when it is no longer valid. A connection is limited to 4 open streams per credential and 16 per server (429 `rate_limited`).

## Events

Existing events keep their names and fields: `run_start` (now with `parent_run_id`), `thinking`, `text`, `tool_start`, `tool_done`, `user`, `plan`, `files`, `run_done` (`{state,error}`, written once when a run ends).

Terminal events are durable and written exactly once, only to the run's own stream:

- `complete`: `{state:"done",thread_id}`
- `run_error`: `{state:"failed",problem:{code,message},effect_ids:[]}`

After the terminal event the server sends the non-durable compatibility event `done` `{state}` and closes. A parked run is not finished: when a stream is caught up with a parked run the server sends `done` `{state:"parked"}` and closes; reconnecting with the last id continues the same stream after the operator answers.

## Retention

Streams of active and parked runs are kept. A finished stream is kept for 30 days after its terminal event; then its events are removed and older cursors answer 410. Deleting a thread deletes its streams.
