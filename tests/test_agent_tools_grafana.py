import json

import httpx
import pytest
from agent_tool_helpers import make_ctx
from helpers import make_config, memory_db

from atrium.agent.tools import grafana
from atrium.agent.tools.base import NotConfigured, ToolError

ENV = {"GRAFANA_URL": "http://grafana.test:3000", "GRAFANA_VIEWER_TOKEN": "viewer"}


def ctx(env=ENV):
    return make_ctx(memory_db(), make_config(__import__("pathlib").Path("/tmp")), env=env)


def serve(monkeypatch, handler):
    seen = []

    def wrapped(request):
        seen.append(request)
        return handler(request)

    monkeypatch.setattr(grafana, "transport", httpx.MockTransport(wrapped))
    return seen


def test_not_configured():
    for env in ({}, {"GRAFANA_URL": "http://x"}, {"GRAFANA_VIEWER_TOKEN": "t"}):
        with pytest.raises(NotConfigured):
            grafana.grafana_datasources(ctx(env), {})


def test_datasources_and_auth_header(monkeypatch):
    seen = serve(monkeypatch, lambda r: httpx.Response(200, json=[{"uid": "p1", "name": "Prom", "type": "prometheus", "url": "secret"}]))
    out = grafana.grafana_datasources(ctx(), {})
    assert out == {"datasources": [{"uid": "p1", "name": "Prom", "type": "prometheus"}]}
    assert seen[0].method == "GET" and seen[0].headers["authorization"] == "Bearer viewer"


def metric_payload(points):
    return {
        "results": {
            "A": {
                "frames": [
                    {
                        "schema": {
                            "fields": [
                                {"name": "Time", "type": "time"},
                                {"name": "Value", "type": "number", "labels": {"instance": "a"}},
                            ]
                        },
                        "data": {"values": [[1_700_000_000_000 + i * 60000 for i in range(points)], [i / 3 for i in range(points)]]},
                    }
                ]
                * 30
            }
        }
    }


def test_query_compacts_frames(monkeypatch):
    seen = serve(monkeypatch, lambda r: httpx.Response(200, json=metric_payload(500)))
    out = grafana.grafana_query(ctx(), {"datasource_uid": "p1", "expr": "up", "step": "60s"})
    assert out["total_series"] == 30 and len(out["series"]) == grafana.MAX_SERIES
    series = out["series"][0]
    assert len(series["rows"]) == grafana.MAX_METRIC_ROWS and series["total_rows"] == 500
    assert series["rows"][0][0] == "2023-11-14T22:13:20+00:00" and series["labels"] == {"instance": "a"}
    assert series["rows"][-1][1] == round(499 / 3, 6)
    body = json.loads(seen[0].content)
    assert seen[0].method == "POST" and seen[0].url.path == "/api/ds/query"
    assert body["from"] == "now-1h" and body["to"] == "now"
    assert body["queries"][0]["datasource"] == {"uid": "p1"} and body["queries"][0]["interval"] == "60s"


def test_logs_query_limits_and_truncates(monkeypatch):
    lines = ["x" * 1000] * 300
    payload = {
        "results": {
            "A": {
                "frames": [
                    {
                        "schema": {"fields": [{"name": "Time", "type": "time"}, {"name": "Line", "type": "string"}, {"name": "id", "type": "string"}]},
                        "data": {"values": [list(range(300)), lines, ["i"] * 300]},
                    }
                ]
            }
        }
    }
    seen = serve(monkeypatch, lambda r: httpx.Response(200, json=payload))
    out = grafana.grafana_query(ctx(), {"datasource_uid": "l1", "expr": '{a="b"}', "kind": "logs", "limit": 5000})
    frame = out["series"][0]
    assert frame["fields"] == ["Time", "Line"] and len(frame["rows"]) == 200
    assert len(frame["rows"][0][1]) <= grafana.CELL_CHARS
    assert json.loads(seen[0].content)["queries"][0]["maxLines"] == 200


def test_query_errors_and_validation(monkeypatch):
    serve(monkeypatch, lambda r: httpx.Response(200, json={"results": {"A": {"error": "parse error"}}}))
    with pytest.raises(ToolError, match="parse error"):
        grafana.grafana_query(ctx(), {"datasource_uid": "p1", "expr": "up"})
    serve(monkeypatch, lambda r: httpx.Response(403, text="forbidden"))
    with pytest.raises(ToolError, match="403"):
        grafana.grafana_query(ctx(), {"datasource_uid": "p1", "expr": "up"})
    with pytest.raises(ToolError):
        grafana.grafana_query(ctx(), {"datasource_uid": "p1", "expr": "up", "kind": "sql"})


def test_alerts_and_dashboards(monkeypatch):
    def handler(request):
        if request.url.path == "/api/prometheus/grafana/api/v1/alerts":
            return httpx.Response(
                200,
                json={"data": {"alerts": [
                    {"state": "firing", "labels": {"alertname": "Down"}, "annotations": {"summary": "host down"}, "activeAt": "t"},
                    {"state": "pending", "labels": {"alertname": "Slow"}},
                ]}},
            )
        return httpx.Response(200, json=[{"title": "Node", "uid": "n1", "url": "/d/n1", "folderTitle": "Infra", "tags": ["a"]}])

    seen = serve(monkeypatch, handler)
    alerts = grafana.grafana_alerts(ctx(), {})
    assert alerts["total_firing"] == 1 and alerts["alerts"][0]["labels"] == {"alertname": "Down"}
    dash = grafana.grafana_search_dashboards(ctx(), {"query": "node"})
    assert dash["dashboards"][0]["uid"] == "n1"
    assert seen[1].url.params["query"] == "node"


def test_only_read_requests_allowed(monkeypatch):
    serve(monkeypatch, lambda r: httpx.Response(200, json={}))
    for method, path in (("POST", "/api/dashboards/db"), ("DELETE", "/api/datasources/1"), ("PUT", "/api/x")):
        with pytest.raises(ToolError, match="read-only"):
            grafana._request(ctx(), method, path)
