#!/usr/bin/env bash
# PROTOTYPE, throwaway. Builds CSS, renders static pages, serves on :8765.
set -euo pipefail
cd "$(dirname "$0")"
mkdir -p out/static/basecoat node_modules
if [ ! -d node_modules/basecoat-css ]; then
  curl -sL https://registry.npmjs.org/basecoat-css/-/basecoat-css-1.0.2.tgz | tar xz && mv package node_modules/basecoat-css
fi
[ -f out/static/htmx.min.js ] || curl -sL https://unpkg.com/htmx.org@2.0.11/dist/htmx.min.js -o out/static/htmx.min.js
cp node_modules/basecoat-css/dist/js/all.min.js out/static/basecoat/all.min.js
cp static/proto.js out/static/proto.js
# same input.css as the app, only the template source dir differs
{ sed "s#source(\"../templates\")#source(\"./tmpl\")#" ../../src/atrium/web/static/input.css; echo "@source \"./static\";"; } > input.css
nix shell nixpkgs#tailwindcss_4 -c tailwindcss --input input.css --output out/static/app.css --minify
mkdir -p out/frag
nix shell nixpkgs#go -c go run .
echo "rendered out/; serving http://127.0.0.1:8765/new-fresh.html"
[ "${1:-}" = "--no-serve" ] || (cd out && python3 -m http.server 8765 --bind 127.0.0.1)
