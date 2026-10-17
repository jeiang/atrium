import hashlib
import json
import os

from . import actual

KIND = "actual_categorize"
ACCOUNT = "actual"
UNDO_OP = "actual_set_category"


class ProposalError(ValueError):
    pass


def _text(raw, key, required=True, limit=300):
    value = raw.get(key)
    if value is None or value == "":
        if required:
            raise ProposalError(f"{key} is required")
        return None
    if not isinstance(value, str):
        raise ProposalError(f"{key} must be a string")
    return value.strip()[:limit] or None


def propose(conn, raw, create=None):
    """Queue a categorization. `description` and `category_name` only label the proposal for the operator."""
    if create is None:
        from . import proposals

        create = proposals.create
    payload = {
        "transaction_id": _text(raw, "transaction_id"),
        "category_id": _text(raw, "category_id"),
        "reason": _text(raw, "reason"),
    }
    description = _text(raw, "description", required=False)
    category_name = _text(raw, "category_name", required=False)
    preview = {**payload, "description": description, "category_name": category_name}
    fingerprint = hashlib.sha256(json.dumps([KIND, payload["transaction_id"], payload["category_id"]]).encode()).hexdigest()[:32]
    title = f"Categorize {description or payload['transaction_id']} as {category_name or payload['category_id']}"
    return create(conn, KIND, ACCOUNT, title, payload, preview, fingerprint)


def _payload(proposal):
    payload = proposal["payload"]
    return json.loads(payload) if isinstance(payload, str) else payload


def execute(conn, cfg, proposal, selection=None):
    from . import proposals

    p = _payload(proposal)
    out = actual.run(os.environ, "set-category", {"transaction_id": p["transaction_id"], "category_id": p["category_id"]})
    proposals.record_action(
        conn,
        proposal["id"],
        {"type": KIND, "transaction_id": p["transaction_id"], "category_id": p["category_id"], "previous_category": out["previous_category"]},
        {"op": UNDO_OP, "transaction_id": p["transaction_id"], "category_id": out["previous_category"]},
    )
    return {"summary": "Recategorized the transaction in Actual", "transaction_id": p["transaction_id"]}


def undo_set_category(conn, cfg, proposal, info):
    actual.run(os.environ, "set-category", {"transaction_id": info["transaction_id"], "category_id": info["category_id"]})
    return {"restored": info["transaction_id"], "category_id": info["category_id"]}


def register(register_executor=None, register_undoer=None):
    if register_executor is None or register_undoer is None:
        from . import proposals

        register_executor = register_executor or proposals.register_executor
        register_undoer = register_undoer or proposals.register_undoer
    register_executor(KIND, execute)
    register_undoer(UNDO_OP, undo_set_category)
