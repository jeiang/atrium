from .base import ToolError


def bounded_int(args, name, default, maximum, minimum=1):
    value = args.get(name, default)
    if isinstance(value, bool) or not isinstance(value, int | float):
        raise ToolError(f"{name} must be a number")
    return max(minimum, min(int(value), maximum))


def text_arg(args, name, required=False):
    value = args.get(name)
    if value is None or value == "":
        if required:
            raise ToolError(f"{name} is required")
        return None
    if not isinstance(value, str):
        raise ToolError(f"{name} must be a string")
    return value.strip() or None


def clip(text, limit):
    text = text or ""
    return text if len(text) <= limit else text[: limit - 1] + "…"
