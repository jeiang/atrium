from . import calendar_proposals, executors, notion_proposals, openloops


def register():
    executors.register()
    notion_proposals.register()
    calendar_proposals.register()
    openloops.register()
