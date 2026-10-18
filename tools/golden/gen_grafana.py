"""Grafana request goldens: every read request of the grafana_* agent tools, answered by a scripted Grafana.
Output: testdata/golden/grafana/ (or argv[1])."""

import json
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import reply, write_slice  # noqa: E402

from atrium.agent.tools import grafana  # noqa: E402
from agent_tool_helpers import make_ctx  # noqa: E402
from helpers import make_config, memory_db  # noqa: E402

SLICE = "grafana"
URL = "http://grafana.test:3000"
ENV = {"GRAFANA_URL": URL + "/", "GRAFANA_VIEWER_TOKEN": "golden-grafana-token"}
FRAME = {
    "schema": {"name": "up", "fields": [{"name": "Time", "type": "time"}, {"name": "Value", "type": "number", "labels": {"instance": "a"}}]},
    "data": {"values": [[1_700_000_000_000, 1_700_000_060_000], [1, 0.5]]},
}
ANSWERS = {
    "/api/datasources": [{"uid": "p1", "name": "Prom", "type": "prometheus", "url": "http://prometheus.invalid"}, {"uid": "l1", "name": "Loki", "type": "loki"}],
    "/api/ds/query": {"results": {"A": {"frames": [FRAME]}}},
    "/api/prometheus/grafana/api/v1/alerts": {"data": {"alerts": [{"labels": {"alertname": "Down"}, "annotations": {"summary": "host down"}, "state": "Alerting", "activeAt": "2026-10-15T12:00:00Z", "value": "0"}]}},
    "/api/search": [{"title": "Node", "uid": "n1", "url": "/d/n1/node", "folderTitle": "Infra", "tags": ["linux"]}],
}


def served(wire):
    wire.serve(URL, lambda method, url, headers, body: reply(200, ANSWERS[url.removeprefix(URL).split("?")[0]]))
    return make_ctx(memory_db(), make_config(Path("/tmp")), env=ENV)


def tools(wire, tmp):
    ctx = served(wire)
    grafana.grafana_datasources(ctx, {})
    grafana.grafana_query(ctx, {"datasource_uid": "p1", "expr": "up"})
    grafana.grafana_query(ctx, {"datasource_uid": "p1", "expr": 'rate(http_requests_total{job="api"}[5m])', "kind": "prometheus", "start": "now-6h", "end": "now-1h", "step": "60s", "limit": 20})
    grafana.grafana_query(ctx, {"datasource_uid": "l1", "expr": '{unit="atrium-web.service"} |= "error"', "kind": "logs", "limit": 150})
    grafana.grafana_alerts(ctx, {})
    grafana.grafana_search_dashboards(ctx, {"query": "node"})
    grafana.grafana_search_dashboards(ctx, {})


def query_error(wire, tmp):
    ctx = served(wire)
    ANSWERS_ERROR = {"results": {"A": {"error": "parse error at char 3", "frames": []}}}
    wire.serve(URL, lambda *a: reply(400, json.dumps(ANSWERS_ERROR), {"Content-Type": "application/json"}))
    try:
        grafana.grafana_query(ctx, {"datasource_uid": "p1", "expr": "up{"})
    except grafana.ToolError:
        return
    raise AssertionError("a 400 must raise")


SCENARIOS = [
    ("tools", "grafana_datasources, grafana_query (prometheus default and with range/step/limit, logs with maxLines), grafana_alerts and grafana_search_dashboards (with and without a query); Bearer viewer token and Accept: application/json on every request.", ["tests/test_agent_tools_grafana.py::test_datasources_and_auth_header", "tests/test_agent_tools_grafana.py::test_query_compacts_frames", "tests/test_agent_tools_grafana.py::test_logs_query_limits_and_truncates", "tests/test_agent_tools_grafana.py::test_alerts_and_dashboards"], tools),
    ("query_error", "A 400 from /api/ds/query raises a ToolError carrying the status and body.", ["tests/test_agent_tools_grafana.py::test_query_errors_and_validation"], query_error),
]

NOTES = ["Only GET requests and POST /api/ds/query are allowed (tests/test_agent_tools_grafana.py::test_only_read_requests_allowed)."]


if __name__ == "__main__":
    write_slice(SLICE, "Grafana", __file__, SCENARIOS, f"`Authorization: Bearer {ENV['GRAFANA_VIEWER_TOKEN']}`", NOTES)
