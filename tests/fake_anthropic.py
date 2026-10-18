from types import SimpleNamespace


def usage(input_tokens=1000, cache_creation=0, cache_read=0, output_tokens=100, searches=0, cache_1h=0, iterations=None):
    return SimpleNamespace(
        input_tokens=input_tokens,
        cache_creation_input_tokens=cache_creation,
        cache_creation=SimpleNamespace(ephemeral_5m_input_tokens=cache_creation - cache_1h, ephemeral_1h_input_tokens=cache_1h),
        cache_read_input_tokens=cache_read,
        output_tokens=output_tokens,
        server_tool_use=SimpleNamespace(web_search_requests=searches, web_fetch_requests=0),
        iterations=iterations,
    )


def iteration(type, input_tokens=0, cache_creation=0, cache_read=0, output_tokens=0):
    return SimpleNamespace(
        type=type,
        input_tokens=input_tokens,
        cache_creation_input_tokens=cache_creation,
        cache_creation=SimpleNamespace(ephemeral_5m_input_tokens=cache_creation, ephemeral_1h_input_tokens=0),
        cache_read_input_tokens=cache_read,
        output_tokens=output_tokens,
    )


def response(content, stop_reason="end_turn", context_management=None, **usage_kwargs):
    return SimpleNamespace(content=content, stop_reason=stop_reason, usage=usage(**usage_kwargs), context_management=context_management)


def compaction(summary, signature="sig-compaction"):
    return {"type": "compaction", "content": summary, "encrypted_content": "enc", "signature": signature}


def cleared(cleared_input_tokens, cleared_tool_uses=1):
    return SimpleNamespace(
        applied_edits=[SimpleNamespace(type="clear_tool_uses_20250919", cleared_input_tokens=cleared_input_tokens, cleared_tool_uses=cleared_tool_uses)]
    )


def text(value, citations=None):
    block = {"type": "text", "text": value}
    if citations:
        block["citations"] = citations
    return block


def thinking(value="pondering", signature="sig-abc"):
    return {"type": "thinking", "thinking": value, "signature": signature}


def tool_use(id, name, /, **args):
    return {"type": "tool_use", "id": id, "name": name, "input": args}


def web_search(id, query, urls):
    return [
        {"type": "server_tool_use", "id": id, "name": "web_search", "input": {"query": query}},
        {
            "type": "web_search_tool_result",
            "tool_use_id": id,
            "content": [{"type": "web_search_result", "url": u, "title": f"Page {i}", "encrypted_content": "x", "page_age": None} for i, u in enumerate(urls)],
        },
    ]


class FakeStream:
    """What `client.beta.messages.stream(...)` yields: SDK-shaped `thinking` and `text` events for the scripted
    response's blocks, then the response itself as the final message."""

    def __init__(self, response):
        self.response = response

    def __enter__(self):
        return self

    def __exit__(self, *exc):
        return False

    def __iter__(self):
        for block in self.response.content:
            if block.get("type") == "thinking" and block.get("thinking"):
                yield SimpleNamespace(type="thinking", thinking=block["thinking"], snapshot=block["thinking"])
            elif block.get("type") == "text":
                yield SimpleNamespace(type="text", text=block["text"], snapshot=block["text"])

    def get_final_message(self):
        return self.response


class FakeClient:
    def __init__(self, responses):
        self.responses = list(responses)
        self.calls = []
        self.messages = self
        self.beta = self

    def create(self, **params):
        self.calls.append(params)
        if not self.responses:
            raise AssertionError("no scripted response left")
        found = self.responses.pop(0)
        if isinstance(found, Exception):
            raise found
        return found

    def stream(self, **params):
        return FakeStream(self.create(**params))
