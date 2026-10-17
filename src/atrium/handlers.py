from . import calendar_proposals, executors, notion_proposals, openloops, receipts


def register():
    executors.register()
    notion_proposals.register()
    calendar_proposals.register()
    openloops.register()
    receipts.register()
