from collections.abc import Callable
from dataclasses import dataclass


def is_partial(request):
    return "hx-request" in request.headers and "hx-history-restore-request" not in request.headers


@dataclass(frozen=True)
class Deps:
    cfg: object
    render: Callable
    page: Callable
    connection: Callable
    toast: Callable
    agent_client: Callable
    env: object
