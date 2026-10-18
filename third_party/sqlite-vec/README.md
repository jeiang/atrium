# sqlite-vec

Vendored for the cgo build of `internal/db` (github.com/mattn/go-sqlite3).

- `sqlite-vec.c`, `sqlite-vec.h`: `sqlite-vec-0.1.9-amalgamation.tar.gz`
  from <https://github.com/asg017/sqlite-vec/releases/tag/v0.1.9>
  (sha256 `3acd67cb4aff080c7050926fd3cf8227905fe5b7ee3829d8ee5024ab1283cf61`),
  unmodified.
- `LICENSE-MIT`, `LICENSE-APACHE`: from the same tag; sqlite-vec is
  dual-licensed MIT or Apache-2.0.
- `sqlite3.h`: SQLite 3.53.4 (public domain), the version
  github.com/mattn/go-sqlite3 v1.14.52 bundles. Bump it with the driver.

`sqlitevec.go` compiles the amalgamation with `SQLITE_CORE` against the
driver's symbols and registers it with `sqlite3_auto_extension`. Flags:
`-D__COSMOPOLITAN__` on Linux (musl lacks `u_int8_t`), `-mavx
-DSQLITE_VEC_ENABLE_AVX` on linux/amd64, `-DSQLITE_VEC_ENABLE_NEON` on arm64.

The pure-Go fallback (`go build -tags modernc`) uses `modernc.org/sqlite/vec`
instead, which bundles the same 0.1.9.
