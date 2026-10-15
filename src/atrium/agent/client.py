from .tools.base import NotConfigured

API_KEY = "ANTHROPIC_API_KEY"


def make_client(env):
    key = (env.get(API_KEY) or "").strip()
    if not key:
        raise NotConfigured(f"{API_KEY} is not set")
    import anthropic

    return anthropic.Anthropic(api_key=key)
