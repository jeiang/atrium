#!/usr/bin/env bash
set -euo pipefail

check=false
if [ "${1:-}" = --check ]; then
  check=true
  shift
fi

root=$(git rev-parse --show-toplevel)
dir=$root/third_party/go-imap
url=$(sed -n 's/^url: //p' "$dir/UPSTREAM")
base=$(sed -n 's/^base: //p' "$dir/UPSTREAM")
ref=${1:-$base}
maxLines=600

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
repo=$work/repo
git clone --quiet "$url" "$repo"
cd "$repo"

export GIT_INDEX_FILE=$work/index
git read-tree "$base"
(cd "$dir" && git --git-dir="$repo/.git" --work-tree=. add --all .)
git rm --cached --quiet UPSTREAM PATCH.md
git diff --cached --no-ext-diff --no-color --binary "$base" >"$work/patch"
added=$(git diff --cached --no-ext-diff --numstat "$base" -- . ':(glob,exclude)**/*_test.go' ':(glob,exclude)**/testdata/**' | awk '{ n += $1 } END { print n + 0 }')
unset GIT_INDEX_FILE
echo "patch on $base: $added added non-test lines (limit $maxLines)"
if [ "$added" -gt "$maxLines" ]; then
  echo "the patch exceeds $maxLines added non-test lines; PATCH.md says to switch to an own client" >&2
  exit 1
fi

commit=$(git rev-parse --verify --quiet "$ref^{commit}" || git rev-parse --verify "origin/$ref^{commit}")
git -c advice.detachedHead=false checkout --quiet "$commit"
echo "target: $ref ($commit)"

if ! git apply --3way --index "$work/patch"; then
  echo "the patch does not apply cleanly onto $ref; resolve it by hand and record the update in PATCH.md" >&2
  exit 1
fi

if [ "$check" = true ]; then
  exit 0
fi

tree=$(git write-tree)
find "$dir" -mindepth 1 -maxdepth 1 ! -name UPSTREAM ! -name PATCH.md -exec rm -rf {} +
git archive "$tree" | tar -x -C "$dir"
if git rev-parse --verify --quiet "refs/tags/$ref" >/dev/null; then
  pinned=$ref
else
  pinned=$commit
fi
printf 'url: %s\nbase: %s\ncommit: %s\n' "$url" "$pinned" "$commit" >"$dir/UPSTREAM"
git -C "$root" status --short -- third_party/go-imap
