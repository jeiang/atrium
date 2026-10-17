import time
from typing import Annotated

from fastapi import Form, HTTPException, Request
from fastapi.responses import RedirectResponse

from ..agent import budget, journal, service, subagents, usage
from ..agent.threads import SUBAGENT
from ..agent.tools.base import CHAT, KINDS
from .deps import is_partial

THREAD_LIMIT = 50
EMPTY_MESSAGE = "Type a message first."


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

    def start(request, thread_id, text):
        # Operator-typed text (the chat form and the Loops "Draft reply" button both post here) is the only
        # input that may add URLs to the thread's web_fetch allowlist.
        try:
            return service.start_run(cfg, thread_id, text, kind=CHAT, client=deps.agent_client(), env=deps.env, operator=True), None
        except budget.BudgetExceeded as e:
            return None, failure(request, "Budget reached", str(e))
        except service.RunActive:
            return None, failure(request, "Still working", "Wait for the current reply to finish, then send again.")
        except service.UnknownThread as e:
            raise HTTPException(404, "unknown thread") from e

    def poll(conn, thread_id, run_id, after, items=(), current=None):
        view = load_thread(conn, thread_id)
        return render("_run_poll.html", view=view, thread_id=thread_id, run_id=run_id, items=list(items), after=after, current=current)

    @app.get("/")
    def home():
        return RedirectResponse("/chat", status_code=303)

    @app.get("/chat")
    def chat_index(request: Request, q: str = "", kind: str = ""):
        q = q.strip()
        if kind and kind not in (*KINDS, SUBAGENT):
            raise HTTPException(422, "unknown kind")
        with connection() as conn:
            found = service.list_threads(conn, q or None, kind or None, THREAD_LIMIT)
            return page(request, "chat.html", "_thread_list.html", threads=found, q=q, kind=kind, kinds=(*KINDS, SUBAGENT))

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

    @app.get("/chat/{thread_id}")
    def chat_thread(request: Request, thread_id: int):
        with connection() as conn:
            view = load_thread(conn, thread_id)
            return page(request, "thread.html", "_thread.html", view=view, thread_id=thread_id, child_runs=subagents.runs_with_children(conn, thread_id))

    @app.post("/chat/{thread_id}/messages")
    def chat_message(request: Request, thread_id: int, text: Annotated[str, Form()] = ""):
        text = text.strip()
        if not text:
            return failure(request, "Nothing sent", EMPTY_MESSAGE)
        with connection() as conn:
            if load_thread(conn, thread_id)["thread"]["kind"] == "subagent":
                raise HTTPException(409, "subagent threads are read-only")
        run_id, failed = start(request, thread_id, text)
        if failed is not None:
            return failed
        if not is_partial(request):
            return RedirectResponse(f"/chat/{thread_id}", status_code=303)
        with connection() as conn:
            items = [i for i in load_thread(conn, thread_id)["items"] if i["run_id"] == run_id]
            return poll(conn, thread_id, run_id, max((i["seq"] for i in items), default=0), items)

    @app.get("/chat/{thread_id}/runs/{run_id}")
    def chat_run(thread_id: int, run_id: int, after: int = 0):
        with connection() as conn:
            found = load_run(conn, thread_id, run_id)
            if found["state"] == journal.RUNNING:
                current = next((c["name"] for c in reversed(found["calls"]) if c["state"] == journal.STARTED), None)
                return poll(conn, thread_id, run_id, after, current=current)
            view = load_thread(conn, thread_id)
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
            last_seq = max((i["seq"] for i in load_thread(conn, thread_id)["items"]), default=0)
            return poll(conn, thread_id, run_id, last_seq)

    @app.get("/usage")
    def usage_page():
        now = time.time()
        with connection() as conn:
            return render(
                "usage.html",
                budget=budget.status(conn, now),
                summary=usage.summary(conn, now),
                by_kind=usage.by_kind(conn, now),
                by_thread=usage.by_thread(conn, now),
            )
