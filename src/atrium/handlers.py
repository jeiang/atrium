from . import executors, notion_proposals


def register():
    executors.register()
    notion_proposals.register()
