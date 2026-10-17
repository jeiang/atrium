import asyncio
import csv
import io
import json
import time
from pathlib import Path
from typing import Annotated

from fastapi import Form, HTTPException, Request
from fastapi.responses import RedirectResponse, Response, StreamingResponse

from ..agent import budget, journal, progress, service, threads, workspace
from ..agent.tools.base import CHAT
from .deps import is_partial

THREAD_LIMIT = 50
EMPTY_MESSAGE = "Type a message first."
ARCHIVED = "archived"
POLL_SECONDS = 0.3
KEEPALIVE_SECONDS = 15.0
PREVIEW_BYTES = 200_000
MARKDOWN_TYPES = ("text/markdown", "text/x-markdown")
CSV_TYPES = ("text/csv", "application/csv")
SSE_HEADERS = {"Cache-Control": "no-cache", "X-Accel-Buffering": "no"}


def sse(kind, data, event_id=None):
    head = f"id: {event_id}\n" if event_id is not None else ""
    return f"{head}event: {kind}\ndata: {json.dumps(data, ensure_ascii=False)}\n\n"


def file_preview(found):
    path = Path(found["path"])
    if not path.is_file():
        return {**found, "render": None, "text": "", "rows": [], "truncated": False}
    raw = path.read_bytes()
    text = raw[:PREVIEW_BYTES].decode("utf-8", errors="replace")
    truncated = len(raw) > PREVIEW_BYTES
    media = found["media_type"]
    suffix = path.suffix.lower()
    if media in MARKDOWN_TYPES or suffix in (".md", ".markdown"):
        return {**found, "render": "markdown", "text": text, "rows": [], "truncated": truncated}
    if media in CSV_TYPES or suffix == ".csv":
        return {**found, "render": "table", "text": "", "rows": list(csv.reader(io.StringIO(text))), "truncated": truncated}
    if media.startswith("text/") or media in ("application/json", "application/xml") or b"\0" not in raw[:4096]:
        return {**found, "render": "text", "text": text, "rows": [], "truncated": truncated}
    return {**found, "render": None, "text": "", "rows": [], "truncated": False}


def register(app, deps):
    cfg, render, page, connection, toast = deps.cfg, deps.render, deps.page, deps.connection, deps.toast

    def load_thread(conn, thread_id):
        view = service.thread_view(conn, thread_id)
        if view is None:
            raise HTTPException(404, "unknown thread")
        return view

    def load_run(conn, thread_id, run_id):
        found = service.run_view(conn, run_id)
        if found is None or found["thread_id"] != thread_id:
            raise HTTPException(404, "unknown run")
        return found

    def failure(request, title, description):
        if is_partial(request):
            return render("_toast.html", status_code=422, toast=toast("error", title, description))
        raise HTTPException(422, description)

    def attempt(request, action):
        try:
            return action(), None
        except budget.BudgetExceeded as e:
            return None, failure(request, "Budget reached", str(e))
        except service.RunActive:
            return None, failure(request, "Still working", "Wait for the current reply to finish, then send again.")
        except service.NothingToRedo as e:
            return None, failure(request, "Nothing to redo", str(e))
        except service.UnknownThread as e:
            raise HTTPException(404, "unknown thread") from e

    def start(request, thread_id, text):
        # Operator-typed text (the chat form and the Loops "Draft reply" button both post here) is the only
        # input that may add URLs to the thread's web_fetch allowlist.
        return attempt(request, lambda: service.start_run(cfg, thread_id, text, kind=CHAT, client=deps.agent_client(), env=deps.env, operator=True))

    def thread_context(conn, thread_id):
        return {"view": load_thread(conn, thread_id), "thread_id": thread_id, "efforts": threads.EFFORTS, "context_limits": threads.CONTEXT_LIMITS}

    def transcript(conn, thread_id, status_code=200, reswap=None):
        reply = render("_transcript.html", status_code=status_code, **thread_context(conn, thread_id))
        if reswap:
            reply.headers["HX-Reswap"] = reswap
        return reply

    @app.get("/")
    def home():
        return RedirectResponse("/chat", status_code=303)

    def listing(request, q, view, status_code=200):
        with connection() as conn:
            found = service.list_threads(conn, q or None, THREAD_LIMIT, view == ARCHIVED)
        context = {"threads": found, "q": q, "view": view}
        if is_partial(request):
            return render("_thread_list.html", status_code=status_code, **context)
        return render("chat.html", status_code=status_code, **context)

    @app.get("/chat")
    def chat_index(request: Request, q: str = "", view: str = ""):
        if view not in ("", ARCHIVED):
            raise HTTPException(422, "unknown view")
        return listing(request, q.strip(), view)

    @app.post("/chat")
    def chat_new(request: Request, text: Annotated[str, Form()] = ""):
        text = text.strip()
        with connection() as conn:
            thread_id = service.create_thread(conn, CHAT)
        if text:
            _, failed = start(request, thread_id, text)
            if failed is not None:
                return failed
        return RedirectResponse(f"/chat/{thread_id}", status_code=303)

    def set_flag(request, thread_id, column, value, q, view, back):
        with connection() as conn:
            if not service.set_thread_flag(conn, thread_id, column, value):
                raise HTTPException(404, "unknown thread")
        if is_partial(request):
            return listing(request, q.strip(), view if view == ARCHIVED else "")
        return RedirectResponse(back, status_code=303)

    @app.post("/chat/{thread_id}/pin")
    def chat_pin(
        request: Request, thread_id: int, pinned: Annotated[int, Form()] = 1, q: Annotated[str, Form()] = "", view: Annotated[str, Form()] = "",
    ):
        return set_flag(request, thread_id, "pinned", bool(pinned), q, view, f"/chat/{thread_id}")

    @app.post("/chat/{thread_id}/archive")
    def chat_archive(
        request: Request, thread_id: int, archived: Annotated[int, Form()] = 1, q: Annotated[str, Form()] = "", view: Annotated[str, Form()] = "",
    ):
        return set_flag(request, thread_id, "archived", bool(archived), q, view, "/chat" if archived else f"/chat/{thread_id}")

    @app.get("/chat/{thread_id}")
    def chat_thread(request: Request, thread_id: int):
        with connection() as conn:
            context = thread_context(conn, thread_id)
            return page(request, "thread.html", "_thread.html", work=workspace.view(conn, thread_id, time.time()), **context)

    @app.post("/chat/{thread_id}/options")
    def chat_options(request: Request, thread_id: int, effort: Annotated[str, Form()] = "", context_limit: Annotated[str, Form()] = ""):
        with connection() as conn:
            try:
                if not threads.set_options(conn, thread_id, effort, context_limit):
                    raise HTTPException(404, "unknown chat thread")
            except threads.OptionError as e:
                return failure(request, "Not saved", str(e))
            if not is_partial(request):
                return RedirectResponse(f"/chat/{thread_id}", status_code=303)
            return render("_meter.html", **thread_context(conn, thread_id))

    @app.post("/chat/{thread_id}/messages")
    def chat_message(request: Request, thread_id: int, text: Annotated[str, Form()] = "", edit: Annotated[str, Form()] = ""):
        text = text.strip()
        if not text:
            return failure(request, "Nothing sent", EMPTY_MESSAGE)
        with connection() as conn:
            if load_thread(conn, thread_id)["thread"]["kind"] == "subagent":
                raise HTTPException(409, "subagent threads are read-only")
        if edit:
            _, failed = attempt(request, lambda: service.edit_last(cfg, thread_id, text, client=deps.agent_client(), env=deps.env))
            if failed is not None:
                return failed
            if not is_partial(request):
                return RedirectResponse(f"/chat/{thread_id}", status_code=303)
            with connection() as conn:
                return transcript(conn, thread_id, reswap="innerHTML")
        outcome, failed = attempt(request, lambda: service.send(cfg, thread_id, text, client=deps.agent_client(), env=deps.env))
        if failed is not None:
            return failed
        if not is_partial(request):
            return RedirectResponse(f"/chat/{thread_id}", status_code=303)
        state, ident = outcome
        with connection() as conn:
            view = load_thread(conn, thread_id)
            if state == "queued":
                return render("_queued.html", view=view, queued=[q for q in view["queued"] if q["id"] == ident])
            items = [i for i in view["items"] if i["run_id"] == ident]
            after = max((i["seq"] for i in items), default=0)
            return render("_run_started.html", view=view, thread_id=thread_id, run_id=ident, items=items, after=after)

    @app.post("/chat/{thread_id}/regenerate")
    def chat_regenerate(request: Request, thread_id: int):
        _, failed = attempt(request, lambda: service.regenerate(cfg, thread_id, client=deps.agent_client(), env=deps.env))
        if failed is not None:
            return failed
        if not is_partial(request):
            return RedirectResponse(f"/chat/{thread_id}", status_code=303)
        with connection() as conn:
            return transcript(conn, thread_id)

    @app.post("/chat/{thread_id}/stop")
    def chat_stop(thread_id: int):
        with connection() as conn:
            active = journal.active_run(conn, thread_id)
            if active is None or not journal.request_stop(conn, active["id"]):
                raise HTTPException(409, "no run is running")
        return Response(status_code=204)

    @app.post("/chat/{thread_id}/branch")
    def chat_branch(thread_id: int, seq: Annotated[int, Form()]):
        with connection() as conn:
            try:
                child = service.branch(conn, thread_id, seq)
            except service.UnknownThread as e:
                raise HTTPException(404, "unknown thread") from e
            except service.NothingToRedo as e:
                raise HTTPException(404, str(e)) from e
        return RedirectResponse(f"/chat/{child}", status_code=303)

    @app.get("/chat/{thread_id}/work")
    def chat_work(thread_id: int):
        with connection() as conn:
            context = thread_context(conn, thread_id)
            return render("_work_update.html", work=workspace.view(conn, thread_id, time.time()), **context)

    @app.get("/chat/{thread_id}/files/{file_id}")
    def chat_file(request: Request, thread_id: int, file_id: int):
        with connection() as conn:
            load_thread(conn, thread_id)
            found = workspace.get_file(conn, thread_id, file_id)
        if found is None:
            raise HTTPException(404, "unknown file")
        return render("_file.html", file=file_preview(found))

    @app.get("/chat/{thread_id}/events")
    async def chat_events(thread_id: int, run: int, after: int = 0):
        with connection() as conn:
            load_run(conn, thread_id, run)

        async def stream():
            last, quiet = after, time.monotonic()
            while True:
                with connection() as conn:
                    events = progress.since(conn, run, last)
                    found = journal.get_run(conn, run)
                for e in events:
                    last = e["id"]
                    yield sse(e["kind"], {"run_id": e["run_id"], **e["data"]}, e["id"])
                if events:
                    quiet = time.monotonic()
                if found is None or found["state"] != journal.RUNNING:
                    yield sse("done", {"state": found["state"] if found else None})
                    return
                if time.monotonic() - quiet >= KEEPALIVE_SECONDS:
                    quiet = time.monotonic()
                    yield ": keepalive\n\n"
                await asyncio.sleep(POLL_SECONDS)

        return StreamingResponse(stream(), media_type="text/event-stream", headers=SSE_HEADERS)

    @app.get("/chat/{thread_id}/runs/{run_id}")
    def chat_run(thread_id: int, run_id: int, after: int = 0):
        with connection() as conn:
            found = load_run(conn, thread_id, run_id)
            view = load_thread(conn, thread_id)
            if found["state"] == journal.RUNNING:
                return render("_run_live.html", view=view, thread_id=thread_id, run_id=run_id, after=after)
            items = [i for i in view["items"] if i["seq"] > after and i["run_id"] == run_id]
            return render("_run_done.html", view=view, thread_id=thread_id, run=found, items=items)

    @app.post("/chat/{thread_id}/runs/{run_id}/resume")
    def chat_resume(request: Request, thread_id: int, run_id: int):
        with connection() as conn:
            load_run(conn, thread_id, run_id)
        try:  # the claim is the only check: two clicks (or the scheduler) racing here resume the run once
            service.start_resume(cfg, run_id, client=deps.agent_client(), env=deps.env)
        except ValueError as e:
            raise HTTPException(409, "run is not interrupted") from e
        if not is_partial(request):
            return RedirectResponse(f"/chat/{thread_id}", status_code=303)
        with connection() as conn:
            view = load_thread(conn, thread_id)
            last_seq = max((i["seq"] for i in view["items"]), default=0)
            return render("_run_live.html", view=view, thread_id=thread_id, run_id=run_id, after=last_seq)
