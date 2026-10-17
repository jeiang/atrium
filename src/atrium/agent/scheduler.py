import logging
import os
import re
import sqlite3
import threading
import time
import uuid
from dataclasses import dataclass
from datetime import datetime, timedelta
from zoneinfo import ZoneInfo

from .. import db, safety
from ..config import TIMEZONE
from . import budget, journal, usage
from .tools.base import CRON, NIGHTLY

log = logging.getLogger("atrium.agent.scheduler")

TICK_SECONDS = 30
LEASE_SECONDS = 1800
MISSED_GRACE_SECONDS = 120
MIN_INTERVAL_SECONDS = 3600
MAX_ACTIVE_JOBS = 20
CHECKED_OCCURRENCES = 64
SEARCH_DAYS = 366 * 5
NIGHTLY_SCHEDULE = "daily 02:30"
NIGHTLY_NAME = "nightly"
BUILTIN = "builtin"

OK = "ok"
ERROR = "error"
SKIPPED_BUDGET = "skipped_budget"
SKIPPED_PAUSE = "skipped_pause"
MISSED_COALESCED = "missed_coalesced"
RUNNING = "running"
STATUSES = (RUNNING, OK, ERROR, SKIPPED_BUDGET, SKIPPED_PAUSE, MISSED_COALESCED)

OPERATOR = "operator"
AGENT = "agent"

SCHEMA = """
CREATE TABLE IF NOT EXISTS jobs (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    prompt TEXT NOT NULL,
    schedule TEXT NOT NULL,
    tz TEXT NOT NULL,
    effort TEXT NOT NULL DEFAULT 'high',
    kind TEXT NOT NULL DEFAULT 'cron',
    enabled INTEGER NOT NULL DEFAULT 1,
    cancelled INTEGER NOT NULL DEFAULT 0,
    next_due REAL,
    lease_owner TEXT,
    lease_until REAL,
    created_by TEXT NOT NULL,
    created_in_thread INTEGER,
    created_at REAL NOT NULL,
    last_run_id INTEGER
);
CREATE INDEX IF NOT EXISTS jobs_due ON jobs (enabled, cancelled, next_due);
CREATE TABLE IF NOT EXISTS job_runs (
    id INTEGER PRIMARY KEY,
    job_id INTEGER NOT NULL REFERENCES jobs(id),
    thread_id INTEGER,
    due_at REAL NOT NULL,
    started_at REAL,
    ended_at REAL,
    status TEXT NOT NULL,
    error TEXT,
    tokens INTEGER NOT NULL DEFAULT 0,
    cost REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS job_runs_job ON job_runs (job_id, id);
"""


def init(conn):
    conn.executescript(SCHEMA)
    # prompt_by: who last wrote the prompt (OPERATOR via the web form, AGENT via the schedule tools, NULL for
    # reminders and prompts that live in code). Only OPERATOR prompts may allowlist URLs for web_fetch.
    if "prompt_by" not in {r["name"] for r in conn.execute("PRAGMA table_info(jobs)")}:
        conn.execute("ALTER TABLE jobs ADD COLUMN prompt_by TEXT")


class ScheduleError(ValueError):
    pass


def tz_name(env=None):
    env = os.environ if env is None else env
    return (env.get("ATRIUM_TZ") or "").strip() or TIMEZONE


def zone(name):
    try:
        return ZoneInfo(name)
    except Exception as e:
        raise ScheduleError(f"unknown timezone {name!r}") from e


DAYS = {"mon": 0, "tue": 1, "wed": 2, "thu": 3, "fri": 4, "sat": 5, "sun": 6}
CRON_BOUNDS = ((0, 59), (0, 23), (1, 31), (1, 12), (0, 7))
CRON_NAMES = {3: {"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12},
              4: {"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}}
TIME = re.compile(r"^(\d{1,2}):(\d{2})$")


def _clock(text):
    m = TIME.match(text)
    if not m or int(m.group(1)) > 23 or int(m.group(2)) > 59:
        raise ScheduleError(f"bad time {text!r}; use HH:MM (24-hour)")
    return int(m.group(1)), int(m.group(2))


def _cron_value(token, index):
    names = CRON_NAMES.get(index, {})
    if token.lower() in names:
        return names[token.lower()]
    if not token.isdigit():
        raise ScheduleError(f"bad cron value {token!r}")
    return int(token)


def _cron_field(text, index):
    low, high = CRON_BOUNDS[index]
    found = set()
    for part in text.split(","):
        body, _, step_text = part.partition("/")
        if step_text and not step_text.isdigit():
            raise ScheduleError(f"bad cron step {part!r}")
        step = int(step_text) if step_text else 1
        if step < 1:
            raise ScheduleError(f"bad cron step {part!r}")
        if body == "*":
            start, end = low, high
            if index == 4:
                end = 6
        elif "-" in body:
            a, _, b = body.partition("-")
            start, end = _cron_value(a, index), _cron_value(b, index)
        else:
            start = _cron_value(body, index)
            end = high if step_text else start
        if start < low or end > high or start > end:
            raise ScheduleError(f"cron value out of range in {part!r}")
        found.update(range(start, end + 1, step))
    if index == 4:
        found = {d % 7 for d in found}
    return frozenset(found)


@dataclass(frozen=True)
class Spec:
    kind: str
    tz: ZoneInfo
    once_at: float | None = None
    minutes: frozenset = frozenset()
    hours: frozenset = frozenset()
    dom: frozenset | None = None
    months: frozenset | None = None
    dow: frozenset | None = None

    @property
    def recurring(self):
        return self.kind != "once"

    def _day_matches(self, day):
        if self.months is not None and day.month not in self.months:
            return False
        cron_dow = (day.weekday() + 1) % 7
        dom_ok = self.dom is None or day.day in self.dom
        dow_ok = self.dow is None or cron_dow in self.dow
        if self.dom is not None and self.dow is not None:
            return dom_ok or dow_ok
        return dom_ok and dow_ok

    def next_after(self, after):
        if self.kind == "once":
            return self.once_at if self.once_at > after else None
        start = datetime.fromtimestamp(after, self.tz)
        times = sorted((h, m) for h in self.hours for m in self.minutes)
        day = start.date()
        for _ in range(SEARCH_DAYS):
            if self._day_matches(day):
                for h, m in times:
                    stamp = datetime(day.year, day.month, day.day, h, m, tzinfo=self.tz).timestamp()
                    if stamp > after:
                        return stamp
            day += timedelta(days=1)
        return None

    def min_gap(self, after):
        if not self.recurring:
            return None
        gaps = []
        previous = self.next_after(after)
        for _ in range(CHECKED_OCCURRENCES):
            if previous is None:
                break
            current = self.next_after(previous)
            if current is None:
                break
            gaps.append(current - previous)
            previous = current
        return min(gaps) if gaps else None


def parse(text, tz):
    text = (text or "").strip()
    if not text:
        raise ScheduleError("schedule is empty")
    tzinfo = zone(tz)
    words = text.split()
    head = words[0].lower()
    if head == "daily":
        if len(words) != 2:
            raise ScheduleError("daily schedules look like 'daily 07:30'")
        h, m = _clock(words[1])
        return Spec("daily", tzinfo, minutes=frozenset({m}), hours=frozenset({h}))
    if head == "weekly":
        if len(words) != 3 or words[1][:3].lower() not in DAYS:
            raise ScheduleError("weekly schedules look like 'weekly mon 07:30'")
        h, m = _clock(words[2])
        day = (DAYS[words[1][:3].lower()] + 1) % 7
        return Spec("weekly", tzinfo, minutes=frozenset({m}), hours=frozenset({h}), dow=frozenset({day}))
    if len(words) == 5:
        fields = [_cron_field(w, i) for i, w in enumerate(words)]
        return Spec(
            "cron",
            tzinfo,
            minutes=fields[0],
            hours=fields[1],
            dom=None if words[2] == "*" else fields[2],
            months=None if words[3] == "*" else fields[3],
            dow=None if words[4] == "*" else fields[4],
        )
    try:
        moment = datetime.fromisoformat(text)
    except ValueError as e:
        raise ScheduleError(
            "unrecognised schedule; use an ISO datetime, 'daily HH:MM', 'weekly DOW HH:MM' or a 5-field cron expression"
        ) from e
    if moment.tzinfo is None:
        moment = moment.replace(tzinfo=tzinfo)
    return Spec("once", tzinfo, once_at=moment.timestamp(), minutes=frozenset(), hours=frozenset())


def validate(text, tz, now, *, enforce_min=True):
    spec = parse(text, tz)
    due = spec.next_after(now)
    if due is None:
        raise ScheduleError("that schedule never fires in the future")
    if enforce_min and spec.recurring:
        gap = spec.min_gap(now)
        if gap is not None and gap < MIN_INTERVAL_SECONDS:
            raise ScheduleError(f"recurring schedules must be at least {MIN_INTERVAL_SECONDS // 3600} hour apart")
    return spec, due


def get_job(conn, job_id):
    row = conn.execute("SELECT * FROM jobs WHERE id = ?", (job_id,)).fetchone()
    return dict(row) if row else None


def active_count(conn):
    return conn.execute("SELECT COUNT(*) AS n FROM jobs WHERE enabled = 1 AND cancelled = 0 AND kind = ?", (CRON,)).fetchone()["n"]


def create_job(conn, *, name, prompt, schedule, tz, now, created_by=OPERATOR, thread_id=None, kind=CRON, enforce=True, prompt_by=None):
    name = (name or "").strip()
    prompt = (prompt or "").strip()
    if not name or not prompt:
        raise ScheduleError("name and prompt are required")
    _, due = validate(schedule, tz, now, enforce_min=enforce)
    if enforce and kind == CRON and active_count(conn) >= MAX_ACTIVE_JOBS:
        raise ScheduleError(f"at most {MAX_ACTIVE_JOBS} active schedules; cancel one first")
    cur = conn.execute(
        "INSERT INTO jobs (name, prompt, schedule, tz, kind, next_due, created_by, created_in_thread, created_at, prompt_by) "
        "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
        (name, prompt, schedule.strip(), tz, kind, due, created_by, thread_id, now, prompt_by),
    )
    return cur.lastrowid


def update_job(conn, job_id, now, *, name=None, prompt=None, schedule=None, enabled=None, enforce=True, prompt_by=None):
    job = get_job(conn, job_id)
    if job is None or job["cancelled"]:
        raise ScheduleError(f"no active schedule {job_id}")
    changes = {}
    if name is not None:
        if not name.strip():
            raise ScheduleError("name is empty")
        changes["name"] = name.strip()
    if prompt is not None:
        if not prompt.strip():
            raise ScheduleError("prompt is empty")
        changes["prompt"] = prompt.strip()
        changes["prompt_by"] = prompt_by
    new_schedule = schedule if schedule is not None else job["schedule"]
    will_enable = bool(job["enabled"]) if enabled is None else bool(enabled)
    if schedule is not None:
        _, due = validate(schedule, job["tz"], now, enforce_min=enforce)
        changes["schedule"] = schedule.strip()
        changes["next_due"] = due if will_enable else job["next_due"]
    if enabled is not None:
        changes["enabled"] = int(will_enable)
        if will_enable and not job["enabled"]:
            if enforce and job["kind"] == CRON and active_count(conn) >= MAX_ACTIVE_JOBS:
                raise ScheduleError(f"at most {MAX_ACTIVE_JOBS} active schedules; cancel one first")
            _, changes["next_due"] = validate(new_schedule, job["tz"], now, enforce_min=False)
    if changes:
        sets = ", ".join(f"{k} = ?" for k in changes)
        conn.execute(f"UPDATE jobs SET {sets} WHERE id = ?", (*changes.values(), job_id))
    return get_job(conn, job_id)


def cancel_job(conn, job_id):
    job = get_job(conn, job_id)
    if job is None or job["cancelled"]:
        raise ScheduleError(f"no active schedule {job_id}")
    if job["kind"] in (NIGHTLY, BUILTIN):
        raise ScheduleError("built-in runs cannot be cancelled; disable them instead")
    conn.execute("UPDATE jobs SET cancelled = 1, enabled = 0, next_due = NULL WHERE id = ?", (job_id,))


def list_jobs(conn, include_cancelled=False):
    where = "" if include_cancelled else "WHERE j.cancelled = 0"
    rows = conn.execute(
        "SELECT j.*, r.status AS last_status, r.thread_id AS last_thread_id, r.started_at AS last_started, r.error AS last_error "
        f"FROM jobs j LEFT JOIN job_runs r ON r.id = j.last_run_id {where} ORDER BY j.kind DESC, j.id"
    )
    return [dict(r) for r in rows]


def ensure_nightly(conn, tz, now):
    from .nightly import NIGHTLY_PROMPT

    if conn.execute("SELECT 1 FROM jobs WHERE kind = ?", (NIGHTLY,)).fetchone():
        return
    create_job(
        conn, name=NIGHTLY_NAME, prompt=NIGHTLY_PROMPT, schedule=NIGHTLY_SCHEDULE, tz=tz, now=now,
        created_by=OPERATOR, kind=NIGHTLY, enforce=False,
    )


def ensure_builtins(conn, tz, now):
    """Seed each built-in job once; an operator who disables one keeps it disabled."""
    from .builtin_jobs import BUILTIN_JOBS

    have = {r["name"] for r in conn.execute("SELECT name FROM jobs WHERE kind = ?", (BUILTIN,))}
    for spec in BUILTIN_JOBS:
        if spec.name not in have:
            create_job(
                conn, name=spec.name, prompt=spec.prompt, schedule=spec.schedule, tz=tz, now=now,
                created_by=OPERATOR, kind=BUILTIN, enforce=False,
            )


@dataclass
class Claim:
    job: dict
    job_run_id: int
    due_at: float
    missed: bool


def _begin(conn):
    conn.execute("BEGIN IMMEDIATE")


def _skip(conn, job, due, status, now):
    cur = conn.execute(
        "INSERT INTO job_runs (job_id, due_at, started_at, ended_at, status) VALUES (?, ?, ?, ?, ?)",
        (job["id"], due, now, now, status),
    )
    return cur.lastrowid


def claim_due(conn, cfg, now, owner):
    """Claim due jobs under a lease; returns (claims, skipped)."""
    claims, skipped = [], []
    paused = safety.is_paused(cfg.pause_path)
    try:
        budget.check(conn, now, True)
        over_budget = False
    except budget.BudgetExceeded:
        over_budget = True
    _begin(conn)
    try:
        rows = conn.execute(
            "SELECT * FROM jobs WHERE enabled = 1 AND cancelled = 0 AND next_due IS NOT NULL AND next_due <= ? "
            "AND (lease_until IS NULL OR lease_until < ?) ORDER BY next_due, id",
            (now, now),
        ).fetchall()
        for row in rows:
            job = dict(row)
            due = job["next_due"]
            missed = now - due > MISSED_GRACE_SECONDS
            spec = parse(job["schedule"], job["tz"])
            following = spec.next_after(now) if spec.recurring else None
            enabled = 1 if following is not None else 0
            if paused or over_budget:
                status = SKIPPED_PAUSE if paused else SKIPPED_BUDGET
                run_id = _skip(conn, job, due, status, now)
                conn.execute("UPDATE jobs SET next_due = ?, enabled = ?, last_run_id = ? WHERE id = ?", (following, enabled, run_id, job["id"]))
                skipped.append({"job": job, "job_run_id": run_id, "status": status})
                continue
            cur = conn.execute(
                "INSERT INTO job_runs (job_id, due_at, started_at, status) VALUES (?, ?, ?, ?)",
                (job["id"], due, now, RUNNING),
            )
            conn.execute(
                "UPDATE jobs SET next_due = ?, enabled = ?, lease_owner = ?, lease_until = ?, last_run_id = ? WHERE id = ?",
                (following, enabled, owner, now + LEASE_SECONDS, cur.lastrowid, job["id"]),
            )
            claims.append(Claim(job, cur.lastrowid, due, missed))
        conn.execute("COMMIT")
    except BaseException:
        conn.execute("ROLLBACK")
        raise
    return claims, skipped


def finish_run(conn, claim, status, now, thread_id=None, error=None, tokens=0, cost=0.0):
    conn.execute(
        "UPDATE job_runs SET status = ?, ended_at = ?, thread_id = COALESCE(?, thread_id), error = ?, tokens = ?, cost = ? WHERE id = ?",
        (status, now, thread_id, error, tokens, cost, claim.job_run_id),
    )
    conn.execute(
        "UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ?",
        (claim.job["id"],),
    )


def run_tokens(conn, run_id):
    row = conn.execute(
        "SELECT COALESCE(SUM(input_tokens + cache_write_5m + cache_write_1h + cache_read + output_tokens), 0) AS n "
        "FROM usage WHERE run_id = ?",
        (run_id,),
    ).fetchone()
    return row["n"]


def execute_claim(cfg, claim, *, client=None, env=None, now=time.time):
    from . import service
    from .builtin_jobs import BUILTIN_JOBS, CALLABLES
    from .nightly import NIGHTLY_PROMPT

    job = claim.job
    if job["kind"] == BUILTIN and job["name"] in CALLABLES:
        return execute_callable(cfg, claim, CALLABLES[job["name"]], client=client, env=env, now=now)
    kind = NIGHTLY if job["kind"] == NIGHTLY else CRON
    prompt = NIGHTLY_PROMPT if kind == NIGHTLY else job["prompt"]
    if job["kind"] == BUILTIN:  # built-in prompts live in code so upgrades reach existing jobs
        prompt = next((s.prompt for s in BUILTIN_JOBS if s.name == job["name"]), prompt)
    operator = job["kind"] == CRON and job["prompt_by"] == OPERATOR
    conn = db.connect(cfg.db_path)
    thread_id = None
    try:
        try:
            thread_id = service.create_thread(conn, kind, title=job["name"], job_id=job["id"], now=now)
            conn.execute("UPDATE job_runs SET thread_id = ? WHERE id = ?", (thread_id, claim.job_run_id))
            run_id = service.run_once(cfg, conn, thread_id, prompt, kind=kind, client=client, env=env, now=now, operator=operator)
        except budget.BudgetExceeded as e:
            finish_run(conn, claim, SKIPPED_BUDGET, now(), thread_id, str(e))
            return SKIPPED_BUDGET
        except Exception as e:
            log.exception("job %s crashed", job["id"])
            finish_run(conn, claim, ERROR, now(), thread_id, f"{type(e).__name__}: {e}")
            return ERROR
        found = journal.get_run(conn, run_id)
        tokens, cost = run_tokens(conn, run_id), usage.run_cost(conn, run_id)
        if found["state"] == journal.DONE:
            status = MISSED_COALESCED if claim.missed else OK
            finish_run(conn, claim, status, now(), thread_id, None, tokens, cost)
        else:
            finish_run(conn, claim, ERROR, now(), thread_id, found["error"] or found["state"], tokens, cost)
            status = ERROR
        return status
    finally:
        conn.close()


def execute_callable(cfg, claim, fn, *, client=None, env=None, now=time.time):
    """Run a built-in Python job; it may start one agent run, whose thread and cost are recorded on the job run."""
    conn = db.connect(cfg.db_path)
    try:
        try:
            result = fn(cfg, conn, client=client, env=env) or {}
        except budget.BudgetExceeded as e:
            finish_run(conn, claim, SKIPPED_BUDGET, now(), None, str(e))
            return SKIPPED_BUDGET
        except Exception as e:
            log.exception("job %s crashed", claim.job["id"])
            finish_run(conn, claim, ERROR, now(), None, f"{type(e).__name__}: {e}")
            return ERROR
        run_id = result.get("run_id")
        found = journal.get_run(conn, run_id) if run_id else None
        thread_id = found["thread_id"] if found else None
        tokens, cost = (run_tokens(conn, run_id), usage.run_cost(conn, run_id)) if found else (0, 0.0)
        if found and found["state"] != journal.DONE:
            finish_run(conn, claim, ERROR, now(), thread_id, found["error"] or found["state"], tokens, cost)
            return ERROR
        status = MISSED_COALESCED if claim.missed else OK
        finish_run(conn, claim, status, now(), thread_id, None, tokens, cost)
        return status
    finally:
        conn.close()


def notify_skipped(conn, env, skip, now):
    from .tools import notify

    job = skip["job"]
    previous = conn.execute(
        "SELECT status FROM job_runs WHERE job_id = ? AND id < ? ORDER BY id DESC LIMIT 1", (job["id"], skip["job_run_id"])
    ).fetchone()
    if skip["status"] != SKIPPED_BUDGET or (previous and previous["status"] == SKIPPED_BUDGET):
        return
    notify.send(conn, env, f"Skipped: {job['name']}", "Monthly budget is above 80%, so this scheduled run did not start.", now=lambda: now)


def tick(cfg, owner, *, client=None, env=None, now=time.time, spawn=None):
    env = os.environ if env is None else env
    spawn = spawn or _spawn
    conn = db.connect(cfg.db_path)
    try:
        stamp = now()
        ensure_nightly(conn, tz_name(env), stamp)
        ensure_builtins(conn, tz_name(env), stamp)
        claims, skipped = claim_due(conn, cfg, stamp, owner)
        for skip in skipped:
            notify_skipped(conn, env, skip, stamp)
    finally:
        conn.close()
    for claim in claims:
        spawn(lambda c=claim: execute_claim(cfg, c, client=client, env=env, now=now), f"atrium-job-{claim.job['id']}")
    return claims, skipped


def _spawn(fn, name):
    thread = threading.Thread(target=fn, name=name, daemon=True)
    thread.start()
    return thread


def release_stale(conn, now):
    """Mark runs left running by a crashed worker once their lease has lapsed."""
    rows = conn.execute(
        "SELECT r.id AS run_id, j.id AS job_id FROM job_runs r JOIN jobs j ON j.id = r.job_id "
        "WHERE r.status = ? AND j.lease_until IS NOT NULL AND j.lease_until < ?",
        (RUNNING, now),
    ).fetchall()
    for row in rows:
        conn.execute("UPDATE job_runs SET status = ?, ended_at = ?, error = 'worker lost' WHERE id = ?", (ERROR, now, row["run_id"]))
        conn.execute("UPDATE jobs SET lease_owner = NULL, lease_until = NULL WHERE id = ?", (row["job_id"],))


def run_worker(cfg, *, client=None, env=None, stop=None, tick_seconds=TICK_SECONDS):
    """The only process that resumes interrupted agent runs (service.resume_interrupted): at startup and then
    every tick, so a web process restart never takes over a run that is still executing elsewhere."""
    from . import service

    stop = stop or threading.Event()
    owner = f"{os.uname().nodename}:{os.getpid()}:{uuid.uuid4().hex[:8]}"
    log.info("scheduler %s started", owner)
    while not stop.is_set():
        try:
            conn = db.connect(cfg.db_path)
            try:
                release_stale(conn, time.time())
            finally:
                conn.close()
            service.resume_interrupted(cfg, client=client, env=env)
            tick(cfg, owner, client=client, env=env)
        except sqlite3.Error:
            log.exception("scheduler tick failed")
        except Exception:
            log.exception("scheduler tick crashed")
        stop.wait(tick_seconds)
