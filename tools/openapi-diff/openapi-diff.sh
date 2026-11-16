#!/usr/bin/env bash
set -euo pipefail

dir=docs/api/baseline
approved=$dir/approved-breaking.txt

if [ "${1:-}" = --baseline ]; then
  tag=${2:?usage: openapi-diff.sh --baseline <tag>}
  commit=$(git rev-parse --verify "$tag^{commit}")
  git show "$commit:docs/api/openapi.json" >"$dir/openapi.json"
  printf 'tag: %s\ncommit: %s\n' "$tag" "$commit" >"$dir/BASELINE"
  echo '# <oasdiff change id> <METHOD> <path> <ruling or decision that approves it>' >"$approved"
  echo "baseline moved to $tag ($commit); approvals reset"
  exit 0
fi

malformed=$(awk 'NF && $1 !~ /^#/ && NF < 4 { printf "%s:%d: %s\n", FILENAME, FNR, $0 }' "$approved")
if [ -n "$malformed" ]; then
  echo "approval lines need: <oasdiff change id> <METHOD> <path> <ruling or decision>" >&2
  echo "$malformed" >&2
  exit 1
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
go tool oasdiff breaking --allow-external-refs=false -f json "$dir/openapi.json" docs/api/openapi.json >"$work/changes.json"

jq --rawfile approvedText "$approved" --arg base "$(sed -n 's/^tag: //p' "$dir/BASELINE")" '
  def orDash: if . == null or . == "" then "-" else . end;
  def key: "\(.id) \(.operation | orDash) \(.path | orDash)";
  ($approvedText | split("\n") | map(select(test("^\\s*(#|$)") | not) | [splits("\\s+")] | map(select(. != "")) | .[0:3] | join(" "))) as $keys
  | map(select(.level >= 2)) as $breaking
  | {
      base: $base,
      approved: ($breaking | map(select(key | IN($keys[]))) | length),
      open: ($breaking | map(select(key | IN($keys[]) | not) | {key: key, level: (if .level == 3 then "ERR" else "WARN" end), where: "\(.operation | orDash) \(.path | orDash)", text})),
      stale: ($keys - ($breaking | map(key)) | unique)
    }
' "$work/changes.json" >"$work/summary.json"

jq -r '
  (if (.stale | length) > 0 then "approval lines that match no change against \(.base); remove them:", (.stale[] | "  \(.)"), "" else empty end),
  (if (.open | length) == 0 then
    "openapi-diff: no unapproved breaking change against \(.base) (\(.approved) approved)"
  else
    "openapi-diff: \(.open | length) unapproved breaking change(s) against \(.base):",
    (.open[] | "  \(.level) \(.where): \(.text)\n    approve with: \(.key) <ruling or decision>"),
    "Restore the contract, or add the approval line to docs/api/baseline/approved-breaking.txt in the same pull request."
  end)
' "$work/summary.json"

jq -e '(.open | length) + (.stale | length) == 0' "$work/summary.json" >/dev/null
