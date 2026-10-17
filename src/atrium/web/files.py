import csv
import io
import time
from pathlib import Path
from typing import Annotated

from fastapi import File, HTTPException, Request, UploadFile
from fastapi.responses import FileResponse

from ..agent import files, projects, service
from ..agent.tools.base import NotConfigured
from .deps import is_partial

PREVIEW_BYTES = 200_000
MARKDOWN_TYPES = ("text/markdown", "text/x-markdown")
CSV_TYPES = ("text/csv", "application/csv")
TABLE_TYPES = (*CSV_TYPES, files.XLSX)


def uploads_of(parts):
    """(name, bytes, declared type) for each non-empty multipart file field; a form with no file picked sends one empty part."""
    found = []
    for part in parts or []:
        if not part.filename:
            continue
        data = part.file.read()
        if data:
            found.append((part.filename, data, part.content_type))
    return found


def file_preview(found):
    path = Path(found["path"])
    media = found["media_type"]
    text = found.get("text")
    truncated = False
    if media in files.IMAGE_TYPES:
        return {**found, "render": "image", "text": "", "rows": [], "truncated": False}
    if text is None:
        if not path.is_file():
            return {**found, "render": None, "text": "", "rows": [], "truncated": False}
        raw = path.read_bytes()
        truncated = len(raw) > PREVIEW_BYTES
        if media.startswith("text/") or media in ("application/json", "application/xml") or b"\0" not in raw[:4096]:
            text = raw[:PREVIEW_BYTES].decode("utf-8", errors="replace")
    elif len(text) > PREVIEW_BYTES:
        text, truncated = text[:PREVIEW_BYTES], True
    if text is None:
        return {**found, "render": None, "text": "", "rows": [], "truncated": False}
    suffix = path.suffix.lower()
    if media in MARKDOWN_TYPES or suffix in (".md", ".markdown"):
        return {**found, "render": "markdown", "text": text, "rows": [], "truncated": truncated}
    if media in TABLE_TYPES or suffix == ".csv":
        return {**found, "render": "table", "text": "", "rows": list(csv.reader(io.StringIO(text))), "truncated": truncated}
    return {**found, "render": "text", "text": text, "rows": [], "truncated": truncated}


def download(found):
    path = Path(found["path"])
    if not path.is_file():
        raise HTTPException(404, "file is missing from storage")
    return FileResponse(path, media_type=found["media_type"], filename=found["name"], content_disposition_type="attachment")


def client_for(deps):
    """The Anthropic client for Files API calls: the app's injected one, else one from the environment, else None."""
    client = deps.agent_client()
    if client is not None:
        return client
    try:
        from ..agent.client import make_client

        return make_client(deps.env)
    except NotConfigured:
        return None


def register(app, deps):
    cfg, render, connection = deps.cfg, deps.render, deps.connection

    def thread_file(conn, thread_id, file_id):
        if service.thread_view(conn, thread_id) is None:
            raise HTTPException(404, "unknown thread")
        found = files.get(conn, file_id)
        if found is None or found["thread_id"] != thread_id:
            raise HTTPException(404, "unknown file")
        return found

    def project_file(conn, project_id, file_id):
        found = files.get(conn, file_id)
        if found is None or found["project_id"] != project_id:
            raise HTTPException(404, "unknown file")
        return found

    def load_project(conn, project_id):
        found = projects.get(conn, project_id)
        if found is None:
            raise HTTPException(404, "unknown project")
        return found

    def project_files(conn, project_id, status_code=200, error=None):
        return render("_project_files.html", status_code=status_code, project=load_project(conn, project_id), files=files.of_project(conn, project_id), error=error)

    @app.get("/chat/{thread_id}/files/{file_id}")
    def chat_file(thread_id: int, file_id: int):
        with connection() as conn:
            found = thread_file(conn, thread_id, file_id)
        return render("_file.html", file=file_preview(found), file_url=f"/chat/{thread_id}/files/{file_id}")

    @app.get("/chat/{thread_id}/files/{file_id}/download")
    def chat_file_download(thread_id: int, file_id: int):
        with connection() as conn:
            found = thread_file(conn, thread_id, file_id)
        return download(found)

    @app.get("/chat/{thread_id}/files/{file_id}/raw")
    def chat_file_raw(thread_id: int, file_id: int):
        """Inline bytes for the image preview only; every other type is served as a download."""
        with connection() as conn:
            found = thread_file(conn, thread_id, file_id)
        if found["media_type"] not in files.IMAGE_TYPES:
            raise HTTPException(404, "not an image")
        return FileResponse(Path(found["path"]), media_type=found["media_type"])

    @app.get("/projects/{project_id}/files")
    def project_files_list(project_id: int):
        with connection() as conn:
            return project_files(conn, project_id)

    @app.post("/projects/{project_id}/files")
    def project_files_upload(request: Request, project_id: int, upload: Annotated[list[UploadFile], File()] = []):
        parts = uploads_of(upload)
        with connection() as conn:
            project = load_project(conn, project_id)
            try:
                if not parts:
                    raise files.FileError("Choose at least one file.")
                files.ingest_uploads(cfg, conn, parts, time.time(), project_id=project_id, context_limit=project["context_limit"])
            except files.FileError as e:
                if is_partial(request):
                    return project_files(conn, project_id, status_code=422, error=str(e))
                raise HTTPException(422, str(e)) from e
            return project_files(conn, project_id)

    @app.get("/projects/{project_id}/files/{file_id}/download")
    def project_file_download(project_id: int, file_id: int):
        with connection() as conn:
            found = project_file(conn, project_id, file_id)
        return download(found)

    @app.post("/projects/{project_id}/files/{file_id}/delete")
    def project_file_delete(project_id: int, file_id: int):
        with connection() as conn:
            project_file(conn, project_id, file_id)
            files.delete_one(cfg, conn, client_for(deps), file_id)
            return project_files(conn, project_id)
