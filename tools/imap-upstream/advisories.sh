#!/usr/bin/env bash
set -euo pipefail

root=$(git rev-parse --show-toplevel)
base=$(sed -n 's/^base: //p' "$root/third_party/go-imap/UPSTREAM")
mod=$(mktemp -d)
trap 'rm -rf "$mod"' EXIT
cd "$mod"

go mod init drift.invalid/goimap
printf 'package goimap\n\nimport _ "github.com/emersion/go-imap/v2/imapclient"\n' >goimap.go
go get "github.com/emersion/go-imap/v2@$base"
go mod tidy
found=$(govulncheck -scan module -format json |
  jq -r 'select(.finding) | .finding | select(.trace[0].module == "github.com/emersion/go-imap/v2") | .osv' |
  sort -u)
if [ -n "$found" ]; then
  echo "advisories against go-imap $base:" >&2
  echo "$found" >&2
  exit 1
fi
echo "no advisories against go-imap $base"
