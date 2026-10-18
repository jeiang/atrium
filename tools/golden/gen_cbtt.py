"""CBTT request goldens: the homepage fetch for the USD selling rate (money.fetch_rates over http.get_text),
answered with the tests/test_money.py page fixture. Output: testdata/golden/cbtt/ (or argv[1])."""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from wire import reply, write_slice  # noqa: E402

from atrium import db, money  # noqa: E402
from atrium.agent import builtin_jobs  # noqa: E402
from helpers import prepare  # noqa: E402

SLICE = "cbtt"
HTML = """
<html><body>
<div class="wrapper"><div class="table_title"> Exchange Rates </div><div class="table_date">2026-10-16</div></div>
<table border="1" class="rate_table"><thead><tr><th></th><th>Buying</th><th>Selling</th></tr></thead><tbody>
<tr><td><p>United States Dollar</p></td><td>6.7312</td><td>6.7648</td></tr>
<tr><td><p>Canadian Dollar</p></td><td>4.7467</td><td>5.3835</td></tr>
</tbody></table>
<div class="wrapper"><div class="table_title"> Inflation Rate </div><div class="table_date">July 2026</div></div>
<table class="rate_table"><tbody><tr><td><p>Headline</p></td><td>0.6</td></tr></tbody></table>
</body></html>
"""


def served(wire):
    wire.serve(money.CBTT_URL, lambda *a: reply(200, HTML, {"Content-Type": "text/html; charset=utf-8"}))


def fx_rates_job(wire, tmp):
    served(wire)
    conn = prepare(db.connect(tmp / "atrium.db"))
    assert builtin_jobs.fx_rates(None, conn)["selling"] == 6.7648


def first_run(wire, tmp):
    served(wire)
    conn = prepare(db.connect(tmp / "atrium.db"))
    assert money.ensure_rates(conn)["selling"] == 6.7648
    assert money.ensure_rates(conn) is None


SCENARIOS = [
    ("fx_rates_job", "The weekly fx-rates builtin job: GET the CBTT homepage with the atrium user agent.", ["tests/test_money.py::test_fx_rates_builtin_job", "tests/test_money.py::test_fetch_and_rate_lookup"], fx_rates_job),
    ("first_run", "money.ensure_rates fetches once while no rate is stored, then not again.", ["tests/test_money.py::test_first_run_fetch_only_when_empty"], first_run),
]


if __name__ == "__main__":
    write_slice(SLICE, "CBTT", __file__, SCENARIOS, "none: the CBTT homepage is public")
