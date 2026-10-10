import json

from .config import LLM_BODY_CHARS, LLM_TOP_SENDERS
from .http import post_json

MODEL = "qwen"
UNCERTAIN = "uncertain"
MAX_TOKENS = 80

FOLDER_SYSTEM = (
    "You file email into mail folders for the mailbox owner. You get the allowed folders with their typical sender "
    "domains, one message, and its 10 most similar already-filed messages (folder, sender domain, subject). "
    "Pick the folder where this message belongs, following how similar messages and the same sender were filed. "
    f'If you cannot tell, answer folder="{UNCERTAIN}". confident=true only if you are sure of the folder.'
)
FLAG_SYSTEM = (
    "You triage the mailbox owner's email and decide whether to flag it. Flag when a person (not an automated "
    "sender) asks the owner something or needs their action, or there is a concrete deadline or payment due, or a "
    "real fraud / sign-in / account-security event. Never flag statements, receipts, newsletters, promotions, or "
    "routine notifications. Answer the JSON fields: person_asking (a human, not an automated sender, asks the owner "
    "something or needs their action), deadline_or_payment_due (concrete deadline or payment due), security_event "
    "(real fraud, sign-in or account-security event), and flag (final decision per the policy)."
)
FLAG_FIELDS = ("person_asking", "deadline_or_payment_due", "security_event", "flag")
FLAG_SCHEMA = {
    "type": "object",
    "properties": {k: {"type": "boolean"} for k in FLAG_FIELDS},
    "required": list(FLAG_FIELDS),
    "additionalProperties": False,
}


def folder_schema(folders):
    return {
        "type": "object",
        "properties": {
            "folder": {"type": "string", "enum": [*folders, UNCERTAIN]},
            "confident": {"type": "boolean"},
        },
        "required": ["folder", "confident"],
        "additionalProperties": False,
    }


def folder_catalog(rows):
    by_folder = {}
    for r in rows:
        by_folder.setdefault(r["folder"], []).append(r["from_domain"])
    lines = []
    for folder in sorted(by_folder):
        counts = {}
        for d in by_folder[folder]:
            counts[d] = counts.get(d, 0) + 1
        top = sorted(counts, key=lambda d: (-counts[d], d))[:LLM_TOP_SENDERS]
        lines.append(f"- {folder} (top senders: {', '.join(top)})")
    return "\n".join(lines)


def folder_prompt(catalog, neighbors, message, body):
    similar = "\n".join(
        f"{i}. [{n['folder']}] from {n['from_domain']}: {(n['subject'] or '')[:100]}"
        for i, n in enumerate(neighbors, 1)
    )
    return (
        f"Allowed folders:\n{catalog}\n\nSimilar filed messages (folder in brackets):\n{similar}\n\n"
        f"Message to file:\nSubject: {message['subject']}\nFrom: {message['from_name']} <{message['from_addr']}>\n"
        f"Body:\n{(body or '')[:LLM_BODY_CHARS]}"
    )


def chat_json(url, system, user, schema):
    reply = post_json(
        f"{url}/chat/completions",
        {
            "model": MODEL,
            "messages": [{"role": "system", "content": system}, {"role": "user", "content": user}],
            "temperature": 0,
            "max_tokens": MAX_TOKENS,
            "cache_prompt": True,
            "chat_template_kwargs": {"enable_thinking": False},
            "response_format": {
                "type": "json_schema",
                "json_schema": {"name": "answer", "strict": True, "schema": schema},
            },
        },
    )
    return json.loads(reply["choices"][0]["message"]["content"])


def judge_folder(url, folders, catalog, neighbors, message, body):
    answer = chat_json(
        url, FOLDER_SYSTEM, folder_prompt(catalog, neighbors, message, body), folder_schema(folders)
    )
    return answer["folder"], bool(answer["confident"])


def judge_flag(url, message, body):
    user = (
        f"From: {message['from_name']} <{message['from_addr']}>\nSubject: {message['subject']}\n"
        f"Body:\n{(body or '')[:LLM_BODY_CHARS]}"
    )
    answer = chat_json(url, FLAG_SYSTEM, user, FLAG_SCHEMA)
    return {k: bool(answer[k]) for k in FLAG_FIELDS}
