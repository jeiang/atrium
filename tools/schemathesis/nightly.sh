#!/usr/bin/env bash
set -euo pipefail

bin=${1:?usage: nightly.sh <atrium binary> <report dir>}
report=${2:?usage: nightly.sh <atrium binary> <report dir>}
version=4.33.4
budget=${SCHEMATHESIS_MAX_TIME:-1800}

bin=$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")
mkdir -p "$report"
report=$(cd "$report" && pwd)
spec=$(git rev-parse --show-toplevel)/docs/api/openapi.json

work=$(mktemp -d)
pid=
trap '[ -z "$pid" ] || { kill "$pid"; wait "$pid"; } 2>/dev/null || true; rm -rf "$work"' EXIT

export HOME=$work/home ATRIUM_DB=$work/atrium.db ATRIUM_CACHE_DIR=$work/cache ATRIUM_INITIAL_TZ=UTC
export ATRIUM_SERVE_ADDR_FILE=$work/addr
mkdir -p "$HOME"

token=$("$bin" auth token issue --name schemathesis)
"$bin" serve --auth local --port 0 >"$report/serve.log" 2>&1 &
pid=$!

base=
for _ in $(seq 1 120); do
  if [ -s "$ATRIUM_SERVE_ADDR_FILE" ] && curl -fsS "http://$(cat "$ATRIUM_SERVE_ADDR_FILE")/healthz" >/dev/null 2>&1; then
    base=http://$(cat "$ATRIUM_SERVE_ADDR_FILE")
    break
  fi
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.5
done
if [ -z "$base" ]; then
  echo "atrium serve did not become healthy" >&2
  cat "$report/serve.log" >&2
  exit 2
fi

start=$(date +%s)
status=0
cd "$work"
timeout $((budget + 300)) uvx --from "schemathesis==$version" schemathesis --no-color run "$spec" \
  --url "$base/api/v1" \
  --header "Authorization: Bearer $token" \
  --include-method GET \
  --checks not_a_server_error,status_code_conformance,content_type_conformance,response_schema_conformance \
  --phases examples,coverage,fuzzing \
  --max-time "$budget" \
  --rate-limit 55/m \
  --request-timeout 20 \
  --output-sanitize true \
  --generation-database none \
  --report junit \
  --report-dir "$report" >"$report/schemathesis.txt" 2>&1 || status=$?
echo "elapsed: $(($(date +%s) - start))s" >>"$report/schemathesis.txt"
cat "$report/schemathesis.txt"
exit "$status"
