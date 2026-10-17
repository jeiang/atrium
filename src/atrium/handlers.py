from . import actual_proposals, calendar_proposals, executors, notion_proposals, openloops, receipts


def register():
    executors.register()
    notion_proposals.register()
    calendar_proposals.register()
    actual_proposals.register()
    openloops.register()
    receipts.register()
