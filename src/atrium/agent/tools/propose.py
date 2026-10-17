from ... import notion_proposals, receipts
from ._args import text_arg
from .base import CHAT, CRON, PROPOSE, Tool, ToolContext, ToolError
from .watches import receipt_proposal


def _page(payload, key):
    value = payload.get(key)
    if not isinstance(value, str) or not value.strip():
        raise ToolError(f"payload.{key} must be a Notion page id")
    return value.strip()


def notion_edit(ctx, payload):
    instruction = payload.get("instruction")
    if not isinstance(instruction, str) or not instruction.strip():
        raise ToolError("payload.instruction must be text")
    return notion_proposals.propose_edit(ctx.conn, ctx.cfg, _page(payload, "page_id"), instruction)


def notion_link(ctx, payload):
    return notion_proposals.propose_link(ctx.conn, ctx.cfg, _page(payload, "source_id"), _page(payload, "target_id"))


# kind -> builder(ctx, payload) returning a proposal id, or None when an identical proposal already exists.
KINDS = {
    notion_proposals.KIND_EDIT: (notion_edit, "{page_id, instruction}: propose an edit to a Notion page for the operator to approve"),
    receipts.KIND: (
        receipt_proposal,
        "{message_id, merchant, amount?, currency?, date?}: propose saving a receipt email to the Notion wiki as a Receipt page",
    ),
    notion_proposals.KIND_LINK: (notion_link, "{source_id, target_id}: propose linking two related Notion pages"),
}


def propose(ctx: ToolContext, args: dict):
    kind = text_arg(args, "kind", required=True)
    payload = args.get("payload")
    if kind not in KINDS:
        raise ToolError(f"unknown proposal kind {kind!r}; allowed: {', '.join(sorted(KINDS))}")
    if not isinstance(payload, dict):
        raise ToolError("payload must be an object")
    try:
        proposal_id = KINDS[kind][0](ctx, payload)
    except notion_proposals.ProposalError as e:
        raise ToolError(str(e)) from e
    if proposal_id is None:
        return {"proposal_id": None, "note": "an identical proposal already exists"}
    return {"proposal_id": proposal_id, "status": "pending", "note": "the operator approves it on the Proposals page"}


TOOLS = [
    Tool(
        name="propose",
        description="Create a pending proposal for the operator to review; nothing is applied until they approve it. Kinds: "
        + "; ".join(f"{k} {d}" for k, (_, d) in sorted(KINDS.items())),
        input_schema={
            "type": "object",
            "properties": {"kind": {"type": "string", "enum": sorted(KINDS)}, "payload": {"type": "object"}},
            "required": ["kind", "payload"],
            "additionalProperties": False,
        },
        handler=propose,
        effect=PROPOSE,
        contexts=frozenset({CHAT, CRON}),
    ),
]
