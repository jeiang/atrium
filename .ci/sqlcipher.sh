#!/usr/bin/env bash
set -euo pipefail

if [ $# -ne 1 ]; then
  echo "usage: .ci/sqlcipher.sh PREFIX" >&2
  exit 2
fi
prefix=$1
pins=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/pins.json
version=$(jq -r .sqlcipher "$pins")
sha256=$(jq -r .sqlcipherSha256 "$pins")
libcrypto=/usr/lib/$(gcc -dumpmachine)/libcrypto.a
if [ ! -e "$libcrypto" ]; then
  sudo apt-get update -qq
  sudo apt-get install -y -qq libssl-dev
fi

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
curl -fsSL --retry 3 -o "$work/src.tar.gz" "https://github.com/sqlcipher/sqlcipher/archive/refs/tags/v$version.tar.gz"
echo "$sha256  $work/src.tar.gz" | sha256sum -c -
mkdir "$work/src"
tar -xzf "$work/src.tar.gz" -C "$work/src" --strip-components=1
cd "$work/src"

flags=(
  -O2
  -DSQLITE_HAS_CODEC
  -DSQLITE_EXTRA_INIT=sqlcipher_extra_init
  -DSQLITE_EXTRA_SHUTDOWN=sqlcipher_extra_shutdown
  -DSQLITE_TEMP_STORE=3
  -DSQLITE_ENABLE_COLUMN_METADATA
  -DSQLITE_ENABLE_DBSTAT_VTAB
  -DSQLITE_ENABLE_JSON1
  -DSQLITE_ENABLE_FTS3
  -DSQLITE_ENABLE_FTS3_PARENTHESIS
  -DSQLITE_ENABLE_FTS3_TOKENIZER
  -DSQLITE_ENABLE_FTS4
  -DSQLITE_ENABLE_FTS5
  -DSQLITE_ENABLE_GEOPOLY
  -DSQLITE_ENABLE_MATH_FUNCTIONS
  -DSQLITE_ENABLE_PERCENTILE
  -DSQLITE_ENABLE_PREUPDATE_HOOK
  -DSQLITE_ENABLE_RBU
  -DSQLITE_ENABLE_RTREE
  -DSQLITE_ENABLE_SESSION
  -DSQLITE_ENABLE_STMT_SCANSTATUS
  -DSQLITE_ENABLE_UNLOCK_NOTIFY
  -DSQLITE_SOUNDEX
  -DSQLITE_SECURE_DELETE
  -DSQLITE_MAX_VARIABLE_NUMBER=250000
  -DSQLITE_MAX_EXPR_DEPTH=10000
)
./configure --enable-threadsafe --enable-load-extension --with-tempstore=yes --disable-tcl \
  CFLAGS="${flags[*]}" LDFLAGS=-lcrypto >/dev/null
make -j"$(nproc)" libsqlite3.a >/dev/null

mkdir -p "$prefix/lib" "$prefix/include"
ar -M <<MRI
create $prefix/lib/libsqlite3.a
addlib libsqlite3.a
addlib $libcrypto
save
end
MRI
cp sqlite3.h sqlite3ext.h "$prefix/include/"
grep -q 'sqlite3_key(' "$prefix/include/sqlite3.h"
