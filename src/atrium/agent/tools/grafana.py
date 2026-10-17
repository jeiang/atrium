from datetime import UTC, datetime

import httpx

from ._args import bounded_int, clip, text_arg
from .base import NotConfigured, Tool, ToolContext, ToolError

TIMEOUT_SECONDS = 15
MAX_SERIES = 20
MAX_METRIC_ROWS = 50
MAX_LOG_ROWS = 200
MAX_ALERTS = 50
MAX_DASHBOARDS = 20
CELL_CHARS = 300
DROP_FIELDS = {"id", "tsNs"}
KINDS = ("prometheus", "logs")
CONTEXTS = frozenset({"chat", "cron", "sub", "code"})

transport = None


def _client(ctx):
    url = (ctx.env.get("GRAFANA_URL") or "").strip().rstrip("/")
    token = (ctx.env.get("GRAFANA_VIEWER_TOKEN") or "").strip()
    if not url or not token:
        raise NotConfigured("GRAFANA_URL and GRAFANA_VIEWER_TOKEN must be set")
    return httpx.Client(
        base_url=url,
        headers={"Authorization": f"Bearer {token}", "Accept": "application/json"},
        timeout=TIMEOUT_SECONDS,
        transport=transport,
    )


def _request(ctx, method, path, **kwargs):
    if method != "GET" and not (method == "POST" and path == "/api/ds/query"):
        raise ToolError("grafana tools are read-only")
    with _client(ctx) as client:
        try:
            response = client.request(method, path, **kwargs)
        except httpx.HTTPError as e:
            raise ToolError(f"grafana unreachable: {type(e).__name__}") from e
    if response.status_code >= 400:
        raise ToolError(f"grafana {response.status_code}: {clip(response.text, 300)}")
    try:
        return response.json()
    except ValueError as e:
        raise ToolError("grafana returned non-JSON") from e


def _cell(value):
    if isinstance(value, float):
        return round(value, 6)
    if isinstance(value, str):
        return clip(value, CELL_CHARS)
    if isinstance(value, dict | list):
        return clip(str(value), CELL_CHARS)
    return value


def _time_cell(value):
    if isinstance(value, int | float) and value > 1e11:
        return datetime.fromtimestamp(value / 1000, UTC).isoformat(timespec="seconds")
    return _cell(value)


def _sample(count, cap):
    if count <= cap:
        return range(count)
    return sorted({round(i * (count - 1) / (cap - 1)) for i in range(cap)})


def compact_frame(frame, row_cap):
    fields = frame.get("schema", {}).get("fields", [])
    columns = frame.get("data", {}).get("values", [])
    keep = [i for i, f in enumerate(fields) if f.get("name") not in DROP_FIELDS and i < len(columns)]
    total = len(columns[0]) if columns else 0
    rows = []
    for r in _sample(total, row_cap):
        rows.append(
            [(_time_cell if fields[i].get("type") == "time" else _cell)(columns[i][r]) for i in keep]
        )
    labels = {}
    for i in keep:
        labels.update(fields[i].get("labels") or {})
    out = {"fields": [fields[i].get("name", "") for i in keep], "rows": rows, "total_rows": total}
    name = frame.get("schema", {}).get("name")
    if name:
        out["name"] = name
    if labels:
        out["labels"] = labels
    return out


def compact_results(payload, row_cap):
    frames = []
    for result in (payload.get("results") or {}).values():
        if result.get("error"):
            raise ToolError(f"query error: {clip(str(result['error']), 300)}")
        frames.extend(result.get("frames") or [])
    return {
        "series": [compact_frame(f, row_cap) for f in frames[:MAX_SERIES]],
        "total_series": len(frames),
    }


def grafana_datasources(ctx: ToolContext, args: dict):
    found = _request(ctx, "GET", "/api/datasources")
    return {"datasources": [{"uid": d.get("uid"), "name": d.get("name"), "type": d.get("type")} for d in found]}


def grafana_query(ctx: ToolContext, args: dict):
    uid = text_arg(args, "datasource_uid", required=True)
    expr = text_arg(args, "expr", required=True)
    kind = text_arg(args, "kind") or "prometheus"
    if kind not in KINDS:
        raise ToolError(f"kind must be one of {', '.join(KINDS)}")
    limit = bounded_int(args, "limit", 100, 200)
    query = {"refId": "A", "datasource": {"uid": uid}, "expr": expr, "maxDataPoints": MAX_METRIC_ROWS}
    if kind == "prometheus":
        query.update({"range": True, "instant": False})
        step = text_arg(args, "step")
        if step:
            query["interval"] = step
        row_cap = min(limit, MAX_METRIC_ROWS)
    else:
        query.update({"queryType": "range", "maxLines": limit})
        row_cap = min(limit, MAX_LOG_ROWS)
    body = {"queries": [query], "from": text_arg(args, "start") or "now-1h", "to": text_arg(args, "end") or "now"}
    return compact_results(_request(ctx, "POST", "/api/ds/query", json=body), row_cap)


def grafana_alerts(ctx: ToolContext, args: dict):
    found = _request(ctx, "GET", "/api/prometheus/grafana/api/v1/alerts")
    alerts = [a for a in (found.get("data") or {}).get("alerts", []) if str(a.get("state", "")).lower() in ("firing", "alerting")]
    return {
        "total_firing": len(alerts),
        "alerts": [
            {
                "labels": a.get("labels", {}),
                "summary": clip((a.get("annotations") or {}).get("summary", ""), CELL_CHARS),
                "active_at": a.get("activeAt"),
                "value": a.get("value"),
            }
            for a in alerts[:MAX_ALERTS]
        ],
    }


def grafana_search_dashboards(ctx: ToolContext, args: dict):
    query = text_arg(args, "query") or ""
    found = _request(ctx, "GET", "/api/search", params={"query": query, "type": "dash-db", "limit": MAX_DASHBOARDS})
    return {
        "dashboards": [
            {
                "title": d.get("title"),
                "uid": d.get("uid"),
                "url": d.get("url"),
                "folder": d.get("folderTitle"),
                "tags": d.get("tags", []),
            }
            for d in found[:MAX_DASHBOARDS]
        ]
    }


def _tool(name, description, handler, properties=None, required=()):
    return Tool(
        name=name,
        description=description,
        input_schema={
            "type": "object",
            "properties": properties or {},
            "required": list(required),
            "additionalProperties": False,
        },
        handler=handler,
        contexts=CONTEXTS,
    )


TOOLS = [
    _tool("grafana_datasources", "List Grafana datasources (uid, name, type).", grafana_datasources),
    _tool(
        "grafana_query",
        "Run a read-only PromQL (kind=prometheus) or LogQL (kind=logs) query via Grafana. Results are compacted to small tables.",
        grafana_query,
        {
            "datasource_uid": {"type": "string"},
            "expr": {"type": "string"},
            "kind": {"type": "string", "enum": list(KINDS)},
            "start": {"type": "string", "description": "e.g. now-1h or epoch ms"},
            "end": {"type": "string"},
            "step": {"type": "string", "description": "Prometheus step, e.g. 60s"},
            "limit": {"type": "integer", "minimum": 1, "maximum": 200},
        },
        ("datasource_uid", "expr"),
    ),
    _tool("grafana_alerts", "List currently firing Grafana alerts.", grafana_alerts),
    _tool(
        "grafana_search_dashboards",
        "Search Grafana dashboards by title.",
        grafana_search_dashboards,
        {"query": {"type": "string"}},
    ),
]
