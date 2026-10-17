from ..agent.tools import notify


def register(app, deps):
    render, connection = deps.render, deps.connection

    @app.get("/notifications")
    def notifications_page():
        with connection() as conn:
            return render("notifications.html", notifications=notify.recent(conn))
