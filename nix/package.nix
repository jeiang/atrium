{
  lib,
  python314Packages,
}: let
  py = python314Packages;
  root = ../.;
in
  py.buildPythonApplication {
    pname = "atrium";
    version = (builtins.fromTOML (builtins.readFile (root + "/pyproject.toml"))).project.version;
    src = lib.fileset.toSource {
      inherit root;
      fileset = lib.fileset.unions [(root + "/pyproject.toml") (root + "/src") (root + "/tests")];
    };
    pyproject = true;
    build-system = [py.setuptools];
    dependencies = [py.anthropic py.caldav py.fastapi py.google-api-python-client py.google-auth py.httpx py.icalendar py.jinja2 py.markdown-it-py py.nh3 py.openpyxl py.pydantic-monty py.python-docx py.python-multipart py.sqlite-vec py.trafilatura py.uvicorn];
    nativeCheckInputs = [py.respx py.pytestCheckHook];
    meta = {
      mainProgram = "atrium";
      license = lib.licenses.mit;
    };
  }
