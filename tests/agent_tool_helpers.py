import time

from atrium.agent.tools.base import ToolContext


def make_ctx(conn, cfg, env=None, kind="chat", allowed_urls=(), embed=None, now=time.time):
    return ToolContext(
        conn=conn,
        cfg=cfg,
        env=env or {},
        kind=kind,
        thread_id=1,
        run_id=1,
        allowed_urls=set(allowed_urls),
        embed=embed,
        now=now,
    )
