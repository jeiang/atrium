import time
from typing import Annotated

from fastapi import Form, HTTPException

from ..agent import skills


def register(app, deps):
    render, connection, toast = deps.render, deps.connection, deps.toast

    def show(notice=None, status_code=200, draft=None):
        with connection() as conn:
            return render("skills.html", skills=skills.list_skills(conn), draft=draft or {}, toast=notice, status_code=status_code)

    @app.get("/skills")
    def skills_page():
        return show()

    @app.post("/skills")
    def skill_save(
        name: Annotated[str, Form()] = "",
        description: Annotated[str, Form()] = "",
        body: Annotated[str, Form()] = "",
    ):
        try:
            with connection() as conn:
                skills.save(conn, name, description, body, time.time())
        except skills.SkillError as e:
            return show(toast("error", "Not saved", str(e)), 422, {"name": name, "description": description, "body": body})
        return show(toast("success", "Skill saved", name.strip()))

    @app.post("/skills/{name}/delete")
    def skill_delete(name: str):
        with connection() as conn:
            if not skills.delete(conn, name):
                raise HTTPException(404, "unknown skill")
        return show(toast("success", "Skill deleted", name))
