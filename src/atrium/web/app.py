import contextlib
import hashlib
import logging
import re
from datetime import UTC, datetime
from pathlib import Path
from typing import Annotated, Literal
from urllib.parse import urlencode, urlsplit

import jinja2
from fastapi import FastAPI, Form, HTTPException, Query, Request
from fastapi.exception_handlers import http_exception_handler
from fastapi.middleware.gzip import GZipMiddleware
from fastapi.responses import HTMLResponse, RedirectResponse
from fastapi.staticfiles import StaticFiles
from starlette.concurrency import run_in_threadpool
from starlette.exceptions import HTTPException as StarletteHTTPException

from .. import (
    applier,
    ask,
    contacts,
    corrections,
    db,
    embed,
    executors,
    handlers,
    live,
    llm,
    message,
    notion_proposals,
    notion_search,
    proposals,
    router,
    ruleedit,
    run,
    rules,
    safety,
    search,
    triage,
    websearch,
)
from ..config import ACCOUNTS
from ..http import ServiceUnavailable
from ..rules import RuleError
from .markup import markdown_html

log = logging.getLogger("atrium.web")
TEMPLATES = Path(__file__).parent / "templates"
PAGE_SIZE = 50
SEARCH_PAGE = 20
LEARNED_LIMIT = 50
ROW, MESSAGE = "row", "message"
TS_FORMATS = {"date": "%Y-%m-%d", "datetime": "%Y-%m-%d %H:%M"}
SEARCH_KEYS = ("q", "account", "folder", "from", "date_from", "date_to", "has_attachment", "unread", "flagged", "source", "private")
FILTER_KEYS = SEARCH_KEYS[1:-2]
NOTION_SOURCE = "notion"
ALL = "all"
PROPOSAL_STATUSES = (
    proposals.PENDING,
    proposals.APPLIED,
    proposals.FAILED,
    proposals.DISMISSED,
    proposals.SUPPRESSED,
    ALL,
)
LIST_OPS = ("in", "suffix")
NO_QUESTION = "Type a question to ask."
NO_NOTION_PROPOSAL = "No proposal was made. The pages may already be linked, or the model saw no reason to change them."
SPLIT_KINDS = (executors.REBALANCE_SPLIT, executors.REBALANCE_MERGE, executors.INBOX_BACKLOG)
GROUPED_KINDS = (executors.REBALANCE_SPLIT, executors.INBOX_BACKLOG)
MESSAGE_KINDS = (executors.DELETION_RULE, executors.GMAIL_LABEL_MERGE)
PROPOSAL_KINDS = (*SPLIT_KINDS, *MESSAGE_KINDS, notion_proposals.KIND_LINK, notion_proposals.KIND_EDIT)
RULE_PROBLEMS = {
    "op eq needs a value": "“is” needs a value.",
    "op re needs a value": "“matches regex” needs a pattern.",
    "op suffix needs a value": "“is or ends in domain” needs at least one domain.",
    "file rule needs dest": "Choose a destination, or set the action to Delete.",
    "when must be a non-empty list": "Add at least one condition.",
    "op in needs a non-empty list of strings": "“is one of” needs at least one value.",
    "op suffix needs a string or list of strings": "“is or ends in domain” needs at least one domain.",
}
BACK_LABELS = {"/": "Search", "/triage": "Triage"}
RULE_ID_PREFIX = re.compile(r"^[^:\s]+: ")


def rule_problem(error):
    text = RULE_ID_PREFIX.sub("", str(error), count=1)
    return RULE_PROBLEMS.get(text, text[:1].upper() + text[1:])


def ts_filter(epoch, style="datetime"):
    if epoch in (None, ""):
        return ""
    moment = datetime.fromtimestamp(epoch, UTC)
    if style == "iso":
        return moment.isoformat()
    return moment.strftime(TS_FORMATS[style])


def asset_version(static_dir):
    digest = hashlib.sha256()
    for path in sorted(Path(static_dir).rglob("*")):
        if path.is_file():
            digest.update(path.read_bytes())
    return digest.hexdigest()[:12]


def build_env(cfg):
    env = jinja2.Environment(loader=jinja2.FileSystemLoader(TEMPLATES), autoescape=True)
    env.filters["ts"] = ts_filter
    env.globals.update(
        asset_version=asset_version(cfg.static_dir),
        mode=cfg.mode,
        accounts=list(ACCOUNTS),
        stages=router.STAGES,
        rule_fields=[*rules.FIELDS, rules.AGE_FIELD],
        rule_ops=(*rules.OPS, rules.AGE_OP),
        valueless_ops=rules.VALUELESS_OPS,
    )
    return env


def is_partial(request):
    return "hx-request" in request.headers and "hx-history-restore-request" not in request.headers


def toast(category, title, description=""):
    return {"category": category, "title": title, "description": description}


def query_url(path, **params):
    live = {k: v for k, v in params.items() if v}
    return f"{path}?{urlencode(live)}" if live else path


def back_link(request):
    ref = urlsplit(request.headers.get("referer", ""))
    label = BACK_LABELS.get(ref.path) if ref.netloc == request.url.netloc else None
    if label is None:
        return {"url": "/", "label": "Search"}
    return {"url": ref.path + (f"?{ref.query}" if ref.query else ""), "label": label}


def create_app(cfg, embed_texts=embed.embed_texts, chat=llm.chat_json, web_answer=websearch.answer):
    if cfg.static_dir is None:
        raise ValueError("ATRIUM_STATIC_DIR is required")
    handlers.register()
    env = build_env(cfg)
    app = FastAPI(docs_url=None, redoc_url=None, openapi_url=None)
    app.add_middleware(GZipMiddleware, minimum_size=1024)
    app.mount("/static", StaticFiles(directory=cfg.static_dir), name="static")

    def pending():
        with connection() as conn:
            return proposals.pending_count(conn)

    def render(name, status_code=200, **context):
        if "pending_count" not in context:
            context["pending_count"] = pending()
        context.setdefault("paused", live.paused(cfg))
        return HTMLResponse(env.get_template(name).render(**context), status_code=status_code)

    def page(request, full, partial, **context):
        if is_partial(request):
            return render(partial, partial=True, **context)
        return render(full, **context)

    @contextlib.contextmanager
    def connection():
        conn = db.connect(cfg.db_path)
        try:
            yield conn
        finally:
            conn.close()

    def folder_map(conn):
        return {name: triage.folders(conn, name) for name in ACCOUNTS}

    def params_of(values):
        return {k: (values.get(k) or "") for k in SEARCH_KEYS}

    def filters_of(params):
        return {k: params[k] for k in FILTER_KEYS}

    def run_search(conn, params, offset):
        limit = offset + SEARCH_PAGE + 1
        if params["source"] == NOTION_SOURCE:
            return notion_search.search(
                conn, cfg, params["q"], include_private=bool(params["private"]), limit=limit, embed_texts=embed_texts
            )
        try:
            return search.search(conn, cfg, params["q"], filters_of(params), limit=limit, embed_texts=embed_texts)
        except ValueError as e:
            return {"hits": [], "semantic": False, "error": str(e), "filters": search.normalize_filters({})}

    @app.get("/")
    def search_page(request: Request, offset: int = 0):
        params = params_of(request.query_params)
        offset = max(offset, 0)
        with connection() as conn:
            results = run_search(conn, params, offset)
            more = len(results["hits"]) > offset + SEARCH_PAGE
            context = {
                "params": params,
                "results": {**results, "hits": results["hits"][offset : offset + SEARCH_PAGE]},
                "offset": offset,
                "more_url": query_url("/", **params, offset=offset + SEARCH_PAGE) if more else None,
            }
            if offset and is_partial(request):
                return render("_notion_hits.html" if params["source"] == NOTION_SOURCE else "_hits.html", **context)
            return page(request, "search.html", "_results.html", folders=folder_map(conn), **context)

    @app.post("/ask")
    def ask_answer(
        q: Annotated[str, Form()] = "",
        account: Annotated[str, Form()] = "",
        folder: Annotated[str, Form()] = "",
        from_: Annotated[str, Form(alias="from")] = "",
        date_from: Annotated[str, Form()] = "",
        date_to: Annotated[str, Form()] = "",
        has_attachment: Annotated[str, Form()] = "",
        unread: Annotated[str, Form()] = "",
        flagged: Annotated[str, Form()] = "",
    ):
        question = q.strip()
        filters = {
            "account": account,
            "folder": folder,
            "from": from_,
            "date_from": date_from,
            "date_to": date_to,
            "has_attachment": has_attachment,
            "unread": unread,
            "flagged": flagged,
        }
        if not question:
            answer = {
                "question": "",
                "answer": "",
                "segments": [],
                "citations": [],
                "rejected": [],
                "sources": [],
                "semantic": False,
                "error": NO_QUESTION,
            }
            return render("_answer.html", answer=answer)
        with connection() as conn:
            try:
                answer = ask.ask(conn, cfg, question, filters, chat=chat, embed_texts=embed_texts)
            except ValueError as e:
                answer = {
                    "question": question,
                    "answer": "",
                    "segments": [],
                    "citations": [],
                    "rejected": [],
                    "sources": [],
                    "semantic": False,
                    "error": str(e),
                }
        return render("_answer.html", answer=answer)

    def load_message(conn, message_id):
        found = message.get_message(conn, cfg, message_id)
        if found is None:
            raise HTTPException(404, "unknown message")
        return found

    @app.get("/messages/{message_id}")
    def message_page(request: Request, message_id: int):
        with connection() as conn:
            found = load_message(conn, message_id)
            return page(request, "message.html", "_message.html", message=found, back=back_link(request))

    def correct_context(conn, found, context, selected=None, error=None):
        return {
            "message_id": found["id"],
            "account": found["account"],
            "context": context,
            "folders": triage.folders(conn, found["account"]),
            "selected": selected,
            "error": error,
        }

    @app.get("/messages/{message_id}/correct")
    def correct_form(message_id: int, context: Literal["row", "message"] = MESSAGE):
        with connection() as conn:
            found = load_message(conn, message_id)
            return render("_correct.html", correct=correct_context(conn, found, context, found["corrected_to"]))

    @app.post("/messages/{message_id}/correct")
    def correct_submit(
        request: Request,
        message_id: int,
        dest: Annotated[str, Form()] = "",
        context: Annotated[Literal["row", "message"], Form()] = MESSAGE,
    ):
        with connection() as conn:
            found = load_message(conn, message_id)
            error = None
            try:
                recorded = corrections.record_correction(conn, message_id, dest)
            except corrections.CorrectionError as e:
                recorded = None
                error = str(e)
            if recorded and not is_partial(request):
                return RedirectResponse(f"/messages/{message_id}", status_code=303)
            moved = move_correction(conn, found, message_id) if recorded else ""
            notice = toast("success", "Correction recorded", f"Corrected to {recorded['folder']}.{moved}") if recorded else None
            fresh = load_message(conn, message_id)
            shown = context if is_partial(request) else MESSAGE
            form = None if recorded else correct_context(conn, fresh, shown, dest.strip() or None, error)
            if context == ROW and is_partial(request):
                row = triage.row(conn, message_id)
                if row is None:
                    raise HTTPException(404, "message has no decision")
                return render("_triage_row.html", row=row, correct=form, toast=notice)
            if is_partial(request):
                return render("_decision_card.html", message=fresh, correct=form, toast=notice)
            return render("message.html", message=fresh, correct=form, back=back_link(request))

    def move_correction(conn, found, message_id):
        if cfg.mode != safety.LIVE:
            return ""
        try:
            with db.exclusive(cfg.db_path):
                done = applier.apply_corrections(conn, cfg, found["account"], run.open_client, [message_id])
        except Exception as e:
            log.exception("moving corrected message %s failed", message_id)
            return f" It could not be moved yet: {type(e).__name__}."
        if not done.ran:
            return " Moving is paused; it will move after you resume."
        if done.written:
            return " The message was moved."
        if done.failed or done.skipped:
            return " It could not be moved; see Triage."
        return ""

    def toggle_pause(request, paused):
        live.set_paused(cfg, paused)
        ref = urlsplit(request.headers.get("referer", ""))
        local = ref.netloc == request.url.netloc and ref.path.startswith("/") and not ref.path.startswith("//")
        target = ref.path + (f"?{ref.query}" if ref.query else "") if local else "/triage"
        return RedirectResponse(target, status_code=303)

    @app.post("/live/pause")
    def live_pause(request: Request):
        return toggle_pause(request, True)

    @app.post("/live/resume")
    def live_resume(request: Request):
        return toggle_pause(request, False)

    @app.get("/actions")
    def actions_page(request: Request, account: str = "", kind: str = "", offset: int = 0):
        offset = max(offset, 0)
        if kind and kind not in live.KINDS:
            raise HTTPException(422, "unknown kind")
        with connection() as conn:
            found = live.list_actions(conn, account or None, kind or None, PAGE_SIZE, offset)
            total = live.count_actions(conn, account or None, kind or None)
            shown = offset + len(found)
            context = {
                "actions": found,
                "account": account,
                "kind": kind,
                "kinds": live.KINDS,
                "total": total,
                "offset": offset,
                "next_offset": shown if shown < total else None,
            }
            if is_partial(request) and offset:
                return render("_action_rows.html", **context)
            return render("actions.html", **context)

    @app.post("/live/actions/{action_id}/undo")
    def live_undo(action_id: int, view: str = ""):
        with connection() as conn:
            found = live.get(conn, action_id)
            if found is None:
                raise HTTPException(404, "unknown action")
            try:
                live.undo(conn, cfg, action_id)
                notice = toast("success", "Undone")
            except live.UndoError as e:
                notice = toast("error", "Not undone", str(e))
            if view == "actions":
                return render("_action_row.html", a=live.get(conn, action_id), toast=notice)
            row = triage.row(conn, found["message_id"])
            if row is None:
                raise HTTPException(404, "message has no decision")
            return render("_triage_row.html", row=row, toast=notice)

    @app.get("/triage")
    def triage_page(request: Request, account: str = "", stage: str = "", agreement: str = "", offset: int = 0):
        offset = max(offset, 0)
        with connection() as conn:
            try:
                found = triage.triage(
                    conn, account or None, stage or None, agreement or None, limit=PAGE_SIZE, offset=offset
                )
            except ValueError as e:
                raise HTTPException(422, str(e)) from e
            shown = offset + len(found["rows"])
            context = {
                "account": account,
                "stage": stage,
                "agreement": agreement,
                "rows": found["rows"],
                "total": found["total"],
                "offset": offset,
                "next_offset": shown if shown < found["total"] else None,
            }
            if is_partial(request):
                return render("_triage_rows.html" if offset else "_decisions.html", **context)
            summaries = [s for s in triage.summaries(conn) if not account or s["account"] == account]
            flags = triage.flag_judgments(conn, account or None, limit=PAGE_SIZE + 1)
            return render(
                "triage.html", summaries=summaries, flags=flags[:PAGE_SIZE], flags_capped=len(flags) > PAGE_SIZE, **context
            )

    def learned_for(conn, account):
        found, capped = [], False
        for name in [account] if account else ACCOUNTS:
            summary = ruleedit.learned_rules_summary(conn, name, LEARNED_LIMIT)
            capped = capped or len(summary) == LEARNED_LIMIT
            found.extend({**r, "account": name} for r in summary)
        found.sort(key=lambda r: (-r["n"], -r["purity"], r["level"], r["key"]))
        return found, capped

    @app.get("/rules")
    def rules_page(account: str = "", saved: str = "", created: str = ""):
        with connection() as conn:
            learned, capped = learned_for(conn, account)
            return render(
                "rules.html",
                account=account,
                rules=ruleedit.list_rules(conn, account or None),
                learned=learned,
                learned_limit=LEARNED_LIMIT if capped else None,
                toast=toast("success", "Rule created" if created else "Rule saved", saved) if saved else None,
            )

    @app.get("/rules/new")
    def rule_new(account: str = "", source_id: Annotated[int | None, Query(alias="message")] = None):
        rule = {"id": "", "account": account or None, "action": "file", "dest": None, "when": []}
        suggestions, source = [], None
        with connection() as conn:
            if source_id is not None:
                try:
                    features = ruleedit.message_features(conn, source_id)
                except RuleError as e:
                    raise HTTPException(404, str(e)) from e
                found = conn.execute("SELECT id, subject, from_addr FROM messages WHERE id = ?", (source_id,)).fetchone()
                source = dict(found)
                rule = {
                    **rule,
                    "account": features["account"],
                    "dest": features["dest"],
                    "when": features["default"],
                }
                suggestions = [c for c in features["conditions"] if c not in features["default"]]
            return render(
                "rule_edit.html",
                rule=rule,
                is_new=True,
                folders=folder_map(conn),
                suggestions=suggestions,
                source_message=source,
                errors=[],
            )

    @app.get("/rules/condition")
    def rule_condition(field: str = "from_addr", op: str = "eq", value: str = "", focus: str = ""):
        return render("_condition_row.html", c={"field": field, "op": op, "value": value}, focus=focus)

    def condition_list(fields, ops, values):
        conditions = []
        for field, op, value in zip(fields, ops, values + [""] * len(fields), strict=False):
            if op in rules.VALUELESS_OPS:
                conditions.append({"field": field, "op": op})
            elif op in LIST_OPS:
                conditions.append({"field": field, "op": op, "value": [v.strip() for v in value.split(",") if v.strip()]})
            elif field == rules.AGE_FIELD and value.strip().isdigit():
                conditions.append({"field": field, "op": op, "value": int(value)})
            else:
                conditions.append({"field": field, "op": op, "value": value})
        return conditions

    def rule_raw(account, action, dest, fields, ops, values):
        return {
            "account": account or None,
            "action": action,
            "dest": (dest or None) if action != "delete" else None,
            "when": condition_list(fields, ops, values),
        }

    @app.post("/rules/preview")
    def rule_preview(
        account: Annotated[str, Form()] = "",
        action: Annotated[str, Form()] = "file",
        dest: Annotated[str, Form()] = "",
        field: Annotated[list[str], Form()] = [],
        op: Annotated[list[str], Form()] = [],
        value: Annotated[list[str], Form()] = [],
    ):
        with connection() as conn:
            try:
                found = ruleedit.preview_rule(conn, {"id": ruleedit.PREVIEW_ID, **rule_raw(account, action, dest, field, op, value)})
            except RuleError as e:
                return render("_rule_preview.html", error=rule_problem(e))
        return render("_rule_preview.html", preview=found)

    def save_rule(conn, rule_id, is_new, form):
        raw = rule_raw(form["account"], form["action"], form["dest"], form["field"], form["op"], form["value"])
        try:
            if is_new:
                rule = ruleedit.create_rule(conn, {"id": rule_id, **raw})
            else:
                rule = ruleedit.update_rule(conn, rule_id, {"id": rule_id, **raw})
        except RuleError as e:
            shown = {"id": rule_id, **raw}
            return render(
                "rule_edit.html",
                rule=shown,
                is_new=is_new,
                folders=folder_map(conn),
                suggestions=[],
                source_message=None,
                errors=[rule_problem(e)],
            )
        done = query_url("/rules", account=form["account"], saved=rule.id, created="1" if is_new else "")
        return RedirectResponse(done, status_code=303)

    @app.post("/rules")
    def rule_create(
        rule_id: Annotated[str, Form(alias="id")] = "",
        account: Annotated[str, Form()] = "",
        action: Annotated[str, Form()] = "file",
        dest: Annotated[str, Form()] = "",
        field: Annotated[list[str], Form()] = [],
        op: Annotated[list[str], Form()] = [],
        value: Annotated[list[str], Form()] = [],
    ):
        form = {"account": account, "action": action, "dest": dest, "field": field, "op": op, "value": value}
        with connection() as conn:
            return save_rule(conn, rule_id.strip(), True, form)

    @app.get("/rules/{rule_id}/edit")
    def rule_edit(rule_id: str):
        with connection() as conn:
            found = next((r for r in ruleedit.list_rules(conn, None) if r["id"] == rule_id), None)
            if found is None:
                raise HTTPException(404, "unknown rule")
            return render(
                "rule_edit.html",
                rule=found,
                is_new=False,
                folders=folder_map(conn),
                suggestions=[],
                source_message=None,
                errors=[],
            )

    @app.post("/rules/{rule_id}")
    def rule_update(
        rule_id: str,
        account: Annotated[str, Form()] = "",
        action: Annotated[str, Form()] = "file",
        dest: Annotated[str, Form()] = "",
        field: Annotated[list[str], Form()] = [],
        op: Annotated[list[str], Form()] = [],
        value: Annotated[list[str], Form()] = [],
    ):
        form = {"account": account, "action": action, "dest": dest, "field": field, "op": op, "value": value}
        with connection() as conn:
            return save_rule(conn, rule_id, False, form)

    @app.post("/rules/{rule_id}/move")
    def rule_move(rule_id: str, direction: Annotated[Literal["up", "down"], Form()], account: Annotated[str, Form()] = ""):
        with connection() as conn:
            ordered = [r["id"] for r in ruleedit.list_rules(conn, account or None)]
            if rule_id not in ordered:
                raise HTTPException(404, "unknown rule")
            index = ordered.index(rule_id)
            target = index - 1 if direction == "up" else index + 1
            if 0 <= target < len(ordered):
                ordered[index], ordered[target] = ordered[target], ordered[index]
                ruleedit.reorder_rules(conn, ordered)
            return render(
                "_rule_list.html",
                account=account,
                rules=ruleedit.list_rules(conn, account or None),
                moved={"id": rule_id, "direction": direction},
            )

    @app.delete("/rules/{rule_id}")
    def rule_delete(rule_id: str, account: str = ""):
        with connection() as conn:
            before = [r["id"] for r in ruleedit.list_rules(conn, account or None)]
            deleted = ruleedit.delete_rule(conn, rule_id)
            notice = (
                toast("success", "Rule deleted", rule_id) if deleted else toast("error", "Rule not found", rule_id)
            )
            remaining = ruleedit.list_rules(conn, account or None)
            index = before.index(rule_id) if rule_id in before else 0
            focus_id = remaining[min(index, len(remaining) - 1)]["id"] if remaining else None
            return render("_rule_list.html", account=account, rules=remaining, toast=notice, focus_id=focus_id)

    @app.get("/contacts")
    def contacts_page(request: Request, q: str = ""):
        everyone = contacts.load_contacts(cfg.contacts_dir)
        return page(
            request,
            "contacts.html",
            "_contacts.html",
            q=q,
            contacts=contacts.filter_contacts(everyone, q),
            total=len(everyone),
        )

    @app.get("/notion/{page_id}")
    def notion_view(request: Request, page_id: str):
        with connection() as conn:
            view = notion_search.page_view(conn, page_id)
            if view is None:
                raise HTTPException(404, "unknown page")
            markdown = view.pop("markdown")
            pages = notion_search.page_titles(conn, [markdown])
            return render(
                "notion_page.html",
                notion_page={**view, "body_html": markdown_html(markdown, pages)},
                related=notion_search.related(conn, view["id"]),
                back=back_link(request),
            )

    def notion_action(propose):
        with connection() as conn:
            try:
                created = propose(conn)
            except (notion_proposals.ProposalError, ServiceUnavailable) as e:
                return render("_notion_action.html", error=str(e))
            if created is None:
                return render("_notion_action.html", error=NO_NOTION_PROPOSAL)
            found = proposals.get(conn, created)
            return render(
                "_notion_action.html", created={"id": found["id"], "kind": found["kind"], "title": found["title"]}
            )

    @app.post("/notion/{page_id}/link")
    def notion_link(page_id: str, target: Annotated[str, Form()]):
        return notion_action(lambda conn: notion_proposals.propose_link(conn, cfg, page_id, target, chat=chat))

    @app.post("/notion/{page_id}/edit")
    def notion_edit(page_id: str, instruction: Annotated[str, Form()] = ""):
        return notion_action(lambda conn: notion_proposals.propose_edit(conn, cfg, page_id, instruction, chat=chat))

    @app.get("/web")
    def web_page(request: Request, q: str = ""):
        q = q.strip()
        if q and is_partial(request):
            return render("_web_answer.html", answer=web_answer(cfg, q, chat=chat, embed_texts=embed_texts), partial=True)
        return render("web.html", q=q)

    @app.get("/proposals")
    def proposals_page(request: Request, status: str = "", kind: str = ""):
        status = status or proposals.PENDING
        if status not in PROPOSAL_STATUSES:
            raise HTTPException(422, "unknown status")
        if kind and kind not in PROPOSAL_KINDS:
            raise HTTPException(422, "unknown kind")
        with connection() as conn:
            context = {
                "proposals": proposals.list(conn, None if status == ALL else status, kind or None),
                "status": status,
                "kind": kind,
                "counts": proposals.counts_by_status(conn),
            }
            if is_partial(request):
                return render("_proposal_list.html", partial=True, **context)
            return render("proposals.html", **context)

    def proposal_card(conn, proposal_id, notice):
        found = proposals.get(conn, proposal_id)
        if found is None:
            raise HTTPException(404, "unknown proposal")
        return render(
            "_proposal.html",
            proposal=found,
            toast=notice,
            pending_oob=True,
            pending_count=proposals.pending_count(conn),
        )

    def picked(form, index, messages):
        if not messages:
            return None
        try:
            return [int(value) for value in form.getlist(f"msg-{index}")]
        except ValueError as e:
            raise proposals.SelectionError("invalid message selection") from e

    def selection_of(proposal, form):
        kind, view = proposal["kind"], proposal["preview"]
        if kind in SPLIT_KINDS:
            groups = view["groups"] if kind in GROUPED_KINDS else [view["group"]]
            return {
                "groups": [
                    {"name": form.get(f"name-{i}"), "message_ids": picked(form, i, g.get("messages"))}
                    for i, g in enumerate(groups)
                ]
            }
        if kind in MESSAGE_KINDS:
            return {"message_ids": picked(form, 0, view.get("messages"))}
        return None

    def approve(proposal_id, form):
        with connection() as conn:
            found = proposals.get(conn, proposal_id)
            if found is None:
                raise HTTPException(404, "unknown proposal")
            try:
                selection = None if form.get("all") else selection_of(found, form)
                done = proposals.approve_and_apply(conn, cfg, proposal_id, selection)
            except proposals.ProposalError as e:
                return proposal_card(conn, proposal_id, toast("error", "Not approved", str(e)))
            result = done["result"] or {}
            if done["status"] == proposals.APPLIED:
                notice = toast("success", "Applied", result.get("summary", ""))
            else:
                notice = toast("error", "It did not finish", result.get("error", ""))
            return proposal_card(conn, proposal_id, notice)

    @app.post("/proposals/{proposal_id}/approve")
    async def proposal_approve(request: Request, proposal_id: int):
        return await run_in_threadpool(approve, proposal_id, await request.form())

    @app.post("/proposals/{proposal_id}/dismiss")
    def proposal_dismiss(proposal_id: int, forever: Annotated[str, Form()] = ""):
        with connection() as conn:
            try:
                proposals.dismiss(conn, proposal_id, bool(forever))
            except proposals.ProposalError as e:
                return proposal_card(conn, proposal_id, toast("error", "Not dismissed", str(e)))
            return proposal_card(conn, proposal_id, toast("success", "Dismissed forever" if forever else "Dismissed"))

    @app.post("/proposals/{proposal_id}/undo")
    def proposal_undo(proposal_id: int):
        with connection() as conn:
            try:
                outcome = proposals.undo(conn, cfg, proposal_id)
            except proposals.ProposalError as e:
                return proposal_card(conn, proposal_id, toast("error", "Not undone", str(e)))
            if outcome["undone"]:
                return proposal_card(conn, proposal_id, toast("success", "Undone"))
            failed = next((o["error"] for o in outcome["outcomes"] if not o["ok"]), "")
            return proposal_card(conn, proposal_id, toast("error", "Undo did not finish", failed))

    @app.exception_handler(StarletteHTTPException)
    async def http_error(request: Request, exc: StarletteHTTPException):
        if exc.status_code not in (404, 422):
            return await http_exception_handler(request, exc)
        problem = f"{exc.detail[:1].upper()}{exc.detail[1:]}."
        context = {"status": exc.status_code, "problem": problem, "path": request.url.path}
        if not is_partial(request):
            return render("error.html", status_code=exc.status_code, **context)
        notice = toast("error", "Filter not understood", problem) if exc.status_code == 422 else None
        return render("_error.html", status_code=exc.status_code, toast=notice, **context)

    return app
