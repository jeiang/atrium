set -euo pipefail
stage=$(mktemp -d)
cp -R "$out/." "$stage/"
printf '{}\n' > assets.json
pack() {
  local name=$1 hash destination
  hash=$(sha256sum "$stage/$name" | cut -c1-16)
  destination="$hash/$name"
  install -Dm444 "$stage/$name" "$out/$destination"
  jq --arg name "$name" --arg path "/static/$destination" '. + {($name): $path}' assets.json > assets.next.json
  mv assets.next.json assets.json
}
while IFS= read -r name; do
  pack "$name"
done < <(cd "$stage"; find . -type f ! -name app.css ! -name manifest.json ! -name sw.js | sed 's|^./||' | LC_ALL=C sort)
jq -r 'to_entries[] | "s|" + (.key | gsub("\\."; "\\.")) + "|" + .value + "|g"' assets.json > assets.sed
sed -f assets.sed "$stage/app.css" > "$stage/app.next.css"
mv "$stage/app.next.css" "$stage/app.css"
sed -f assets.sed "$stage/manifest.json" > "$stage/manifest.next.json"
mv "$stage/manifest.next.json" "$stage/manifest.json"
pack app.css
pack manifest.json
jq -S . assets.json > "$out/assets.json"
assets=$(jq -cS . assets.json)
sed "s|__ATRIUM_ASSETS__|$assets|" "$stage/sw.js" > sw.next.js
rm -f "$out/sw.js"
install -Dm444 sw.next.js "$out/sw.js"
while IFS= read -r file; do
  case "$file" in
    *.css|*.js|*.json|*.svg)
      gzip -9n -c "$file" > "$file.gz"
      [ "$(wc -c < "$file.gz")" -lt "$(wc -c < "$file")" ] || rm "$file.gz"
      brotli -q 11 -c "$file" > "$file.br"
      [ "$(wc -c < "$file.br")" -lt "$(wc -c < "$file")" ] || rm "$file.br"
      ;;
  esac
done < <(find "$out" -type f | LC_ALL=C sort)
