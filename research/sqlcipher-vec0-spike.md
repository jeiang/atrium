# SQLCipher with vec0: static build spike and benchmark

Date: 2026-10-18. Ticket: [#76](https://github.com/jeiang/atrium/issues/76). Builds on
[#51](https://github.com/jeiang/atrium/blob/research/sqlite-driver-matrix/research/sqlite-driver-matrix.md) (driver matrix),
[#23](https://github.com/jeiang/atrium/blob/research/encrypted-sqlite/research/encrypted-sqlite.md) (encrypted SQLite) and the #72 decision
(mattn/go-sqlite3 + vendored sqlite-vec 0.1.9, musl static, x86-64-v3 / arm64).

## 1. Answer in short

* **It ships as one static binary.** mattn/go-sqlite3 (its `libsqlite3` build tag) + nixpkgs SQLCipher 4.19.0 with static OpenSSL 3.6.4 + vendored
  sqlite-vec 0.1.9 + FTS5, musl, `-march=x86-64-v3`, built with `pkgsStatic.buildGoModule`: 10.0 MB stripped, `statically linked`, opens/creates an
  encrypted DB with vec0 + FTS5, passes `integrity_check` (section 4). No BitBox fork is needed, but one 12-line patch to mattn is (key must be applied
  before mattn's own pragmas; section 4.1).
* **Encryption cost is a cache-size problem, not a crypto problem.** With a small page cache (SQLite default 2 MB) OpenSSL-backed SQLCipher is
  6x slower on KNN and 9x slower on vec0 inserts than plaintext. With a page cache at least as large as the hot vec0 partition (64 MB for a
  10k-row partition, 128 MB for 30k rows) encrypted == plaintext within noise, and encrypted KNN is even faster than plaintext mattn (section 4).
* **OpenSSL vs LibTomCrypt, same host, same program:** 3.3x faster inserts (46.7 s vs 154 s) and 3.2x faster KNN (56 ms vs 181 ms) at the small
  cache; no difference at a large cache. The cost that remains is per-page HMAC-SHA512 (not AES): turning the HMAC off cuts it 3x, and mc's ChaCha20-Poly1305
  is 2x faster than SQLCipher's default.
* **SQLite3 Multiple Ciphers works too** (v2.6.1, SQLite 3.54.0, amalgamation compiled by us into `libsqlite3.a`): 4.7 MB binary, URI `hexkey=` works with
  stock mattn, default cipher ChaCha20-Poly1305 is 2x faster than SQLCipher on the cold path and equal on the warm path.
* **Multi-process WAL under encryption works**: 2 writers + 2 readers + a checkpointer in 5 processes, kill -9 mid-transaction, crash recovery: 0 errors,
  `integrity_check` ok, WAL file is encrypted, `-shm` holds no page data (section 6).
* **Threat model:** whole-file encryption buys little over disk encryption on a host you control, and nothing against a compromised host; it matters for
  hosts with no disk encryption (artemis today), VPS deployments, and copies that leave the box (section 2).

## 2. Threat model (defined first)

Facts about atrium and its host:

* `atrium.db` holds mail bodies and embeddings, Notion pages, threads and agent transcripts, money data (`src/atrium/db.py:6,87-90`; schema in `db.py`). Integration
  secrets live in the environment file, not in the DB (`grep` of `src/atrium/db.py` finds no token/secret column; the runbook says the host supplies secrets,
  `AGENTS.md:7`).
* Artemis's root and `/persist` are plain btrfs: `lsblk -o NAME,TYPE,FSTYPE` shows `nvme3n1p1 btrfs /` with no `crypt` layer, and `cryptsetup` is not installed
  (measured on artemis 2026-10-18). Backups are restic to S3 with a repository password (`cornn-flaek/modules/backups/default.nix:11-13,54-55,112-121`), which restic encrypts client-side.
* Restic taking a btrfs read-only snapshot (`modules/backups/default.nix:176-180`) means plaintext DB pages exist in snapshots; encryption introduced later does not remove
  plaintext history from old snapshots or restic generations (already noted in #23).

| Attacker / event | Whole-file encryption (key in a file on the same host) | Host disk encryption (LUKS/ZFS) | Encrypted backups (restic) | Encrypted secrets table |
|---|---|---|---|---|
| Stolen/RMA'd/decommissioned disk | defeats it for the DB only | defeats it for everything | n/a | tokens only |
| Leaked snapshot or copy outside restic (rsync, `btrfs send`, debug copy, support bundle) | defeats it for the DB | no (decrypted at mount) | no | tokens only |
| Other OS user/container that can read the file but not the key file | defeats it (if key perms differ) | no | no | tokens only |
| Arbitrary-file-read bug (path traversal) in the web app | defeats it unless the key file is also reachable | no | no | tokens only |
| Compromised atrium process or root | no (key is in memory and on disk) | no | no | no |
| Stolen restic repo | no change (already encrypted) | no change | defeats it | n/a |
| Tampering with the DB file at rest | HMAC modes detect it (SQLCipher default, mc ChaCha20-Poly1305); mc `aes256cbc` does not | no | restic detects it | no |

Reading: app-level whole-file encryption is a defence in depth for (a) hosts without disk encryption, (b) a VPS you cannot encrypt, and (c) DB copies that leave the host unprotected.
It is not a defence against a host or process compromise, and it does not replace LUKS where LUKS is available (LUKS covers the Maildir/Notion caches too).
It protects mail bodies, which a secrets table cannot. These are the facts for the owner; whether to ship it is #76's recommendation below.

## 3. What was built and measured

Host: artemis (AMD Ryzen 7 7800X3D, 8 cores, 93 GB RAM, NixOS, btrfs on NVMe, `x86_64-linux`), shared with the live atrium, so absolute numbers are noisy
(plaintext vec0 insert at the default cache varied 2.9-5.4 s between runs; p50 KNN varied under 2%). Throwaway dir `/tmp/sc76`, deleted afterwards.

Toolchain: Go 1.26.7, gcc 15.3 musl (`nixpkgs#pkgsStatic.stdenv.cc`, nixpkgs rev `4975466d`), mattn/go-sqlite3 v1.14.52, sqlite-vec 0.1.9 amalgamation
([release](https://github.com/asg017/sqlite-vec/releases/tag/v0.1.9)), SQLCipher 4.19.0 from nixpkgs (SQLite 3.53.4, OpenSSL 3.6.4 static),
SQLite3 Multiple Ciphers 2.6.1 amalgamation ([release](https://github.com/utelle/SQLite3MultipleCiphers/releases/tag/v2.6.1), SQLite 3.54.0),
BitBoxSwiss/go-sqlite3-sqlcipher `d03fd26` (SQLCipher 4.16.0, LibTomCrypt) for the baseline from #51. All builds: CGO, `-linkmode external -extldflags -static`,
`-march=x86-64-v3 -DSQLITE_VEC_ENABLE_AVX`, FTS5 on, vec0 registered with `sqlite3_auto_extension`.

Benchmark program (one Go file, all variants): synthetic DB like #51: `message_vec` vec0 (`account` partition key, 768-d `FLOAT[768]` cosine, 200 Gaussian clusters, normalised),
`messages` + external-content FTS5 `messages_fts` (120 random words per body, 5000-word vocabulary). WAL, `synchronous` at the default (FULL), `_txlock=immediate`, `busy_timeout=30000`,
one connection, raw 256-bit key (no PBKDF2). Workloads: vec0 insert in batches of 64 (embed_pending shape), messages+FTS5 insert in 5000-row transactions, KNN k=40 on one partition
(100 random queries, p50, two passes, both passes agree), FTS5 `bm25`+`snippet` limit 50 (300 queries, p50), `quick_check`. Row counts: 20 000 (partition of 10 000 rows, DB 88 MB) and
60 000 (partition of 30 000, DB 265 MB; #51's real DB had 60k+4k vec rows at 533 MB). Data are synthetic (same caveat as #51).

### 3.1 20 000 vec0 rows, SQLite default page cache (2 MB, `cache_size=-2000`): the pessimistic case

| Build | vec0 insert 20k | KNN k=40 (10k partition) | FTS5 p50 | vs plaintext |
|---|---|---|---|---|
| plaintext mattn, bundled SQLite, x86-64-v3 | 5.4 s (3.7k rows/s) | 9.4 ms | 0.22 ms | 1x |
| plaintext + `mmap_size=1G` | 2.9 s | 5.9 ms | 0.21 ms | |
| SQLCipher 4.19 **LibTomCrypt** (BitBox fork, 4.16) | **154.3 s** (130 rows/s) | **180.8 ms** | 0.74 ms | 29x / 19x |
| SQLCipher 4.19 **OpenSSL**, 4 KB pages, AES-256-CBC + HMAC-SHA512 (default) | **46.7 s** (428 rows/s) | **56.5 ms** | 0.37 ms | 8.7x / 6.0x |
| SQLCipher OpenSSL, `cipher_hmac_algorithm=HMAC_SHA256` | 32.0 s | 39.6 ms | 0.31 ms | |
| SQLCipher OpenSSL, 16 KB pages | 32.1 s | 42.0 ms | 0.60 ms | |
| SQLCipher OpenSSL, 16 KB pages + HMAC-SHA256 | 19.5 s | 26.7 ms | 0.45 ms | |
| SQLCipher OpenSSL, `cipher_use_hmac=OFF` (no tamper detection) | 12.5 s | 18.4 ms | 0.24 ms | |
| mc ChaCha20-Poly1305 (default), 4 KB | 23.7 s (844 rows/s) | 29.9 ms | 0.29 ms | 4.4x / 3.2x |
| mc ChaCha20-Poly1305, 16 KB | 17.5 s | 24.1 ms | 0.43 ms | |
| mc `cipher=sqlcipher legacy=4` (SQLCipher 4 format, mc's own AES) | 51.5 s | 61.0 ms | 0.38 ms | |
| mc `aes256cbc` (wxSQLite3; **no MAC**, IV derived from page number) | 12.8 s (1561 rows/s) | 17.5 ms | 0.24 ms | |
| mc `aes256cbc`, 16 KB | 7.3 s | 12.1 ms | 0.31 ms | |

The LibTomCrypt row reproduces #51's finding on this host (#51: 75.8 s and 741 ms on a different machine, single 20k partition). OpenSSL cuts the penalty by 3.2-3.3x, but a 2 MB cache keeps
it well above acceptable. 60 000 rows at the default cache (30k partition) scale linearly: plaintext 16.4 s / 29.9 ms; SQLCipher OpenSSL 142.2 s / **172.9 ms**; mc ChaCha20 72.3 s / 90.0 ms.

### 3.2 Page cache large enough for the hot partition: the realistic case

| Build (20k rows, 10k partition) | cache_size | vec0 insert 20k | KNN k=40 | FTS5 |
|---|---|---|---|---|
| plaintext mattn | 256 MB | 1.32 s | 9.4 ms | 0.19 ms |
| plaintext + `mmap_size=1G` | 64 MB | 1.29 s | 5.9 ms | 0.22 ms |
| SQLCipher OpenSSL | 256 MB | 1.59 s | 6.1 ms | 0.16 ms |
| SQLCipher OpenSSL | 64 MB | 1.64 s | 6.6 ms | 0.19 ms |
| SQLCipher LibTomCrypt | 64 MB | 2.07 s | 6.6 ms | 0.18 ms |
| SQLCipher OpenSSL, 16 KB pages | 64 MB | 1.04 s | 6.5 ms | 0.23 ms |
| mc ChaCha20 / sqlcipher-compat / aes256cbc | 256 MB | 1.48 / 2.00 / 1.47 s | 6.1 / 6.1 / 6.1 ms | 0.19 / 0.17 / 0.20 ms |

| Build (60k rows, 30k partition, 265-272 MB) | cache_size | vec0 insert 60k | KNN k=40 | FTS5 | quick_check |
|---|---|---|---|---|---|
| plaintext mattn | 128 MB / 1 GB | 3.95 s / 3.54 s | 28.4 / 29.0 ms | 0.40 ms | 0.39 / 0.34 s |
| SQLCipher OpenSSL | 128 MB / 1 GB | 4.81 s / 4.68 s | 20.2 / 19.0 ms | 0.61 / 0.42 ms | 0.75 / 0.38 s |
| mc ChaCha20 | 128 MB / 1 GB | 4.74 s / 4.58 s | 20.7 / 19.3 ms | 0.49 / 0.41 ms | 0.48 / 0.31 s |
| mc sqlcipher-compat / aes256cbc | 1 GB | 6.18 / 4.50 s | 20.0 / 19.9 ms | 0.40 / 0.40 ms | 0.31 / 0.31 s |

Messages+FTS5 insert (5000-row transactions) is insensitive: about 25-36k rows/s plaintext vs 21-32k rows/s encrypted at every cache size; encrypted files are 2.5-3% larger (HMAC/IV reserve).

Why the encrypted build can beat plaintext on KNN `[inference]`: vec0 reads each 3 MB chunk blob through SQLite's incremental-blob API. A plaintext DB in WAL mode can serve overflow pages by
direct file reads that bypass the page cache (a `pread` copy of ~30 MB per query, 9.4 ms); the encrypted build must go through the pager, so a warm cache is a zero-copy hit (6 ms).
Plaintext with `mmap_size=1G` reaches the same 5.9 ms, supporting this. Likewise the slow cold path is `[inference]` page decryption: each vec0 insert/KNN walks the chunk's overflow chain
(about 750 pages for one chunk) and with a 2 MB cache every page is decrypted again on every access. Evidence for the cache explanation: same code, same data, cost vanishes between a 2 MB and a 64 MB cache.

What dominates the cold-cache cost: AES is cheap, HMAC is not. `openssl speed -bytes 4096` on this host: AES-256-XTS 17.3 GB/s, ChaCha20-Poly1305 5.0 GB/s, SHA-256 2.3 GB/s, SHA-512 1.14 GB/s,
AES-256-CBC (encrypt, serial) 1.28 GB/s. SQLCipher with HMAC off (AES only) is 3x faster than with HMAC-SHA512.

### 3.3 Key derivation and wrong-key behaviour (measured, SQLCipher OpenSSL build)

* Raw key (`PRAGMA key="x'<64 hex>'"`): open + check in a fresh process 16 ms. Passphrase key (256k PBKDF2 iterations by default per [SQLCipher design](https://www.zetetic.net/sqlcipher/design/)): 90 ms, so about 75 ms per new pooled connection. Use a raw key.
* Wrong key, no key on an encrypted file, plaintext file + key: `file is not a database`; SQLCipher also prints `ERROR CORE sqlcipher_page_cipher: hmac check failed for pgno=1` to stderr by default.
* **Silent downgrade hazards, both measured:** (1) a plaintext DB opened *without* a key by the SQLCipher build opens and works as plaintext; (2) the plain mattn build given `PRAGMA key="secret"` raises no error and writes a plaintext file (header `SQLite format 3`). A startup guard is mandatory: require `PRAGMA cipher_version` non-empty (proves the SQLCipher library is linked), and refuse if the first 16 bytes of the file are `SQLite format 3\0`.

## 4. Build recipe (proven: `nix-build` produced a working static binary)

Layout used for the proof (`default.nix` next to `go.mod`, `main.go`, `vec/` with `sqlite-vec.c/.h` + 8-line cgo wrapper, `mattn-patched/`, `vendor/`):

```nix
# sqlcipher-static.nix: nixpkgs' pkgsStatic.sqlcipher fails to build as-is (libsqlite3.so link under musl static, measured),
# so build only the archive. Crypto provider is OpenSSL (static, from pkgsStatic).
let pkgs = import <nixpkgs> { }; in
pkgs.pkgsStatic.sqlcipher.overrideAttrs (old: {
  configureFlags = old.configureFlags ++ [ "--disable-shared" "--enable-static" ];
  buildFlags = [ "libsqlite3.a" ];          # library only; the CLI needs static readline/ncurses
  installPhase = ''
    mkdir -p $out/lib $out/include/sqlcipher
    cp libsqlite3.a $out/lib/libsqlcipher.a
    cp sqlite3.h sqlite3ext.h $out/include/sqlcipher/
  '';
  postInstall = "";
})
```

```nix
# default.nix
let
  pkgs = import <nixpkgs> { };
  sqlcipher = import ./sqlcipher-static.nix;
  openssl = pkgs.pkgsStatic.openssl;
  # mattn's `libsqlite3` tag links -lsqlite3; hand it SQLCipher under that name.
  libsqlite3 = pkgs.runCommand "libsqlite3-sqlcipher" { } ''
    mkdir -p $out/lib; ln -s ${sqlcipher}/lib/libsqlcipher.a $out/lib/libsqlite3.a
  '';
in pkgs.pkgsStatic.buildGoModule {
  pname = "atrium"; version = "0";
  src = ./.; subPackages = [ "." ];
  vendorHash = null;                          # `go mod vendor` output committed; mattn replaced by the patched tree
  tags = [ "libsqlite3" "sqlite_fts5" ];
  env = {
    CGO_ENABLED = "1";
    CGO_CFLAGS  = "-I${sqlcipher}/include/sqlcipher -march=x86-64-v3 -DSQLITE_VEC_ENABLE_AVX -O2";
    CGO_LDFLAGS = "-L${libsqlite3}/lib -L${openssl.out}/lib -lcrypto";
  };
  ldflags = [ "-s" "-w" "-linkmode external" "-extldflags=-static" ];
}
```

sqlite-vec wrapper (vendored `sqlite-vec.c/.h` 0.1.9 next to it; `-D__COSMOPOLITAN__` skips sqlite-vec's `u_int8_t` typedefs that musl lacks, as in #51; `SQLITE_CORE` makes it use `sqlite3.h` instead of the loadable-extension API):

```go
package vec
/*
#cgo CFLAGS: -DSQLITE_CORE -DSQLITE_VEC_STATIC -D__COSMOPOLITAN__ -O3
#include "sqlite-vec.h"
static int atrium_vec_auto(void) { return sqlite3_auto_extension((void (*)(void))sqlite3_vec_init); }
*/
import "C"
func Auto() { C.atrium_vec_auto() }
```

Results: plaintext variant (mattn's bundled SQLite, same flags) 4.3 MB; SQLCipher+OpenSSL+vec 10.0 MB (OpenSSL libcrypto adds about 5.7 MB; trimming it is untested);
SQLite3MC 4.7 MB; LibTomCrypt fork 4.4 MB (all stripped, static, bench program). `ldd`: not a dynamic executable.

### 4.1 Gotchas found

1. **mattn runs its own pragmas before `ConnectHook`.** `Open` always executes `PRAGMA busy_timeout`, `locking_mode` and `synchronous` before the hook (`sqlite3.go:1665,1874,1906` at v1.14.52). On an existing encrypted file the first schema-touching
    pragma fails with `file is not a database`, so key-in-`ConnectHook` works for *creating* a DB (what #51 and my first run did) but **not for reopening**. Fix used here (12 lines in `sqlite3.go`, DSN param `_sqlcipher_key`):
    ```go
    // in Open: parse params.Get("_sqlcipher_key"); then, before the busy_timeout exec:
    if sqlcipherKey != "" { if err := exec("PRAGMA key = " + sqlcipherKey + ";"); err != nil { return fail(err) } }
    ```
    mattn's own [PR #1109](https://github.com/mattn/go-sqlite3/pull/1109) (SQLCipher support, still open, updated 2026-07-01) and [issue #1306](https://github.com/mattn/go-sqlite3/issues/1306) are the upstream track. Options: vendor a patched copy via `replace` (done here), keep a tiny fork, or
    carry the patch in the Nix build. With SQLite3MC no patch is needed: SQLite3MC reads `cipher=` and `hexkey=` from the URI (`file:x.db?hexkey=<64 hex>`), which mattn passes through for `file:` DSNs (tested; reopen and multi-process runs used it).
2. **nixpkgs `pkgsStatic.sqlcipher` fails** (`libsqlite3.so` link under musl static); override as above. nixpkgs already builds SQLCipher with `SQLITE_TEMP_STORE=3` and `-DSQLITE_HAS_CODEC`, FTS5 comes through `sqlite.NIX_CFLAGS_COMPILE` (confirmed: `fts5=true` in `PRAGMA compile_options`).
3. **SQLite3MC on gcc 15 + musl:** `-std=gnu17` is required (`memset_explicit` is C23 and absent from musl; the amalgamation selects it under `__STDC_VERSION__ >= 202311L`). Compiled with `-O2 -march=x86-64-v3 -maes -mpclmul -DSQLITE_ENABLE_FTS5 -DSQLITE_THREADSAFE=1 -DSQLITE_ENABLE_COLUMN_METADATA -DSQLITE_ENABLE_MATH_FUNCTIONS -DSQLITE_USE_URI=1` in 30 s, archived as `libsqlite3.a`, same `libsqlite3` tag path.
4. mattn's `libsqlite3` tag against SQLCipher/SQLite3MC compiled cleanly; mattn's per-feature `#cgo CFLAGS` still apply to its own files but the SQL engine comes from the archive. The `sqlite_fts5` tag has no effect on the engine in this mode (FTS5 comes from how the archive was compiled), so FTS5 must be in the archive's flags. `PRAGMA compile_options` is the runtime check (the benchmark prints it).
5. `PRAGMA key` must precede any read; do not use mattn's `_journal_mode` DSN parameter (#51); set WAL after the key (done in the hook here).
6. `cipher_memory_security=OFF` made no measurable difference to the cold-cache numbers (57.7 ms KNN, 47.0 s insert).

## 5. SQLite3 Multiple Ciphers as the alternative

* Source: [utelle/SQLite3MultipleCiphers](https://github.com/utelle/SQLite3MultipleCiphers) v2.6.1 (2026-10-12, pushed to 2026-10-17, MIT, 687 stars, one main maintainer), amalgamation `sqlite3mc_amalgamation.c` (13 MB) plus SQLite 3.54.0. Ciphers: ChaCha20-Poly1305 default
  ([sqleet design](https://utelle.github.io/SQLite3MultipleCiphers/docs/ciphers/cipher_chacha20/): per-page key from the page number and a nonce, 16-byte Poly1305 tag per page, PBKDF2-SHA256 for passphrases), SQLCipher-compatible AES-256, wxSQLite3 AES-256 without HMAC
  ([page](https://utelle.github.io/SQLite3MultipleCiphers/docs/ciphers/cipher_aes256cbc/): "does not use a HMAC"), and others.
* Measured: works with vec0 + FTS5 through mattn `libsqlite3` (tables 3.1/3.2); WAL multi-process and crash recovery ok (section 6). Cold-cache cost is about half of SQLCipher's (ChaCha20-Poly1305 is not slowed by SHA-512); warm cost identical.
* Cons: its default format is not SQLCipher 4, so the stock `sqlcipher` CLI/DB Browser cannot open it (compat mode `cipher=sqlcipher;legacy=4` can, at SQLCipher-like speed: 51.5 s, 61 ms cold); no maintained Go binding ([mattn #1109](https://github.com/mattn/go-sqlite3/pull/1109) open, `jgiannuzzi/go-sqlite3` last pushed 2026-02-27);
  SQLite version is whatever the release bundles (3.54.0 vs 3.53.x elsewhere), which interacts with #72's "pin at or above the writer's SQLite" rule; an upgrade path is "download the next amalgamation".
* SQLCipher: [BSD-3 community edition](https://github.com/sqlcipher/sqlcipher), Zetetic-maintained, 4.19.0 released 2026-09-08, in nixpkgs, standard on-disk format ([design](https://www.zetetic.net/sqlcipher/design/): AES-256-CBC, per-page IV, HMAC-SHA512 per page, WAL/journals encrypted, temp files only safe with temp_store memory).

## 6. Multi-process WAL, checkpoint and crash behaviour under encryption (measured)

Program roles (separate processes on one DB, `-reopen`, 2 MB cache, `_txlock=immediate`, `busy_timeout=30000`): 2 writers (8 messages + FTS rows + vec0 rows per transaction), 2 readers (KNN k=40 + FTS count), 1 checkpointer
(`wal_checkpoint(PASSIVE)` every 500 ms), 10 s; then `integrity_check`. Then 2 writers + 1 reader with `kill -9` of two writers mid-run (WAL left behind) and a fresh-process `integrity_check`.

| Build | ops, 10 s (writers: txns, readers: queries) | errors | after kill -9: integrity | plaintext runs of 12+ printable bytes in `.db` / `-wal` / `-shm` after the kills |
|---|---|---|---|---|
| plaintext | writers 863+2120, readers 1096+1097 | 0 | ok | 25 555 / 27 120 / 0 (header `SQLite format 3`) |
| SQLCipher OpenSSL (raw key) | writers 469+161, readers 387+390 | 0 | ok (11 536 rows, FTS and vec0 counts consistent) | 217 / 432 / 0 (random-data coincidences; header random) |
| SQLite3MC ChaCha20 (`hexkey=`) | writers 398+749, readers 552+553 | 0 | ok | 344 / 413 / 0 |

(The two-writer imbalance is the immediate-transaction wait ordering; encrypted throughput is lower here only because the processes use a 2 MB cache.)

Findings:

* No `SQLITE_BUSY`, no "file is not a database", no HMAC failures with several processes sharing one encrypted file, one key, WAL on. WAL frames are encrypted; `-shm` carries no page content.
* Crash recovery (kill -9 with a populated WAL) is the normal SQLite recovery; counts of `messages`, `messages_fts_docsize` and `message_vec_rowids` agree.
* The `atrium.db.lock` flock (`src/atrium/db.py:215-221`) is a separate file; it is unaffected by encryption. Encryption adds no new locking rules.
* **WAL growth (affects plaintext too):** with readers always active, WAL files reached 180-800 MB during the test (plaintext 770 MB, SQLCipher 182 MB, mc 213 MB) because the autocheckpoint cannot finish while snapshots are held and the test's PASSIVE checkpointer never ran after the first phase.
  Atrium today sets no `wal_autocheckpoint` or `journal_size_limit` (`db.py:198-205`). Under encryption the file just stays larger; checkpoint copies encrypted frames without recryption (SQLCipher's page format is the same in the WAL and in the DB, per the design page). Policy candidate: `journal_size_limit`, a TRUNCATE checkpoint in a quiet window from `atrium-run`.

## 7. Options and recommendation

| Option | vec0 + FTS5 | Speed (cold 2 MB / warm) | Static one-binary | Format/tooling | Verdict |
|---|---|---|---|---|---|
| **SQLCipher 4.19 + OpenSSL** via mattn `libsqlite3` | yes (measured) | 6x / 1x | yes, 10 MB, needs 12-line mattn patch + nixpkgs override | standard SQLCipher 4, HMAC, widely audited, CLI works | **recommended** |
| SQLCipher + LibTomCrypt (BitBox fork) | yes | 19x / 1x | yes, 4.4 MB, fork | same format | rejected: cold-cache cost 3x worse, fork maintenance (HEAD 2026-07-03, 0 stars) |
| SQLite3MC 2.6.1 (ChaCha20-Poly1305) | yes | 3x / 1x | yes, 4.7 MB, no mattn patch needed | not SQLCipher; compat mode exists but is slow | credible alternative if binary size or no-patch matters more than tooling |
| SQLite3MC `aes256cbc` | yes | 1.9x / 1x | yes | no MAC, deterministic IV | not recommended (no tamper detection) |
| Adiantum VFS (ncruces) | yes, on frozen v0.20 only | 2.6x KNN (#51) | pure Go | no MAC, no rekey | not re-measured; stuck on the 2024 stack (#51) |
| LUKS/ZFS-native | n/a | not measured: no sudo/dm-crypt here; AES-XTS is 17.3 GB/s on this CPU, and the page cache sits above dm-crypt `[inference]`, so the warm cost is nil | n/a | whole-disk, ops-owned | best protection per cost where you own the host; independent of the DB |

Recommendation, tied to the plan (Phase B, key from a `*_file`, DB encrypted at rest):

1. Ship **SQLCipher 4.19 (OpenSSL) through mattn's `libsqlite3` tag**, raw 256-bit key (`PRAGMA key="x'..'"`) from a `*_file` secret, HMAC on (default), `temp_store=MEMORY`. Keep 4 KB pages: a 16 KB page file did not beat it enough to justify a non-default.
2. **Set the page cache from the vector working set, not the default.** Rule of thumb from the data: a vec0 partition costs about 3 KB per 768-d row (30k rows ≈ 92 MB). Use a larger cache on the one connection that runs KNN (128-256 MB for atrium's real size) and a small cache (8-16 MB) on the rest, because `cache_size` is per connection.
    Without it, expect 170 ms instead of 20-30 ms per KNN on a 30k partition. A cache that does not fit hurts encrypted vec0 inserts far more (142 s for 60k rows).
3. Add the startup guard (section 3.3) and a plaintext-to-encrypted offline migration (`atrium db encrypt`, as #23 recommended), verified by counts and KNN equality; `sqlcipher_export` for vec0/FTS5 tables was not re-tested here.
4. Decide the plaintext-vs-whole-file question with the threat model: artemis has no LUKS. If the owner wants one control, LUKS on artemis costs nothing at runtime; app-level encryption is for portable installs (VPS, hosts without disk encryption).

## 8. Risks and unknowns

* Single host, one run per cell for inserts; shared with live atrium. KNN p50s are stable; insert times are not (plaintext 2.9-5.4 s for the same load).
* Synthetic vectors and text (same caveat as #51).
* Not measured: arm64 (musl cross, NEON, OpenSSL arm64 asm), LUKS, ZFS, Adiantum on this host, `sqlcipher_export` of vec0/FTS5, rekey, backup API under encryption, SQLCipher with a populated 533 MB real DB, many pooled connections' aggregate cache RSS and `mlock` limits under SQLCipher memory security with a large cache (a 1 GB cache ran fine here).
* The mattn patch must be carried or upstreamed; [PR #1109](https://github.com/mattn/go-sqlite3/pull/1109) has been open since 2022-11-10 and is still unmerged.
* OpenSSL static adds 5.7 MB and an OpenSSL CVE cadence to the release process; SQLite3MC has no OpenSSL.
* sqlite-vec is pre-1.0 and its blob I/O pattern (not the crypto) is what makes the cache size matter; ANN/IVF alternatives would change this.

## 9. Candidate new tickets

| Title | Type | Phase | Area | Question |
|---|---|---|---|---|
| Startup key and format guard | build | B | db | Refuse to start if `cipher_version` is empty with a key configured, or if the file header is plaintext while a key is configured; wrong-key message without SQLCipher's raw stderr log (`cipher_log` off). Both silent-downgrade cases in 3.3 get a test |
| Carry or upstream the mattn key patch | task | B | db/build | Vendor `_sqlcipher_key` (or the PR #1109 branch) via `replace`, or fork; pin in the Nix build |
| Page-cache budget and connection topology | spec | B | db | One vector connection with a 128-256 MB cache, small cache for the others; memory budget on the VPS and `LimitMEMLOCK` vs SQLCipher memory security with large caches |
| Encrypt-in-place migration test on the real 533 MB DB | spike | B | db | `sqlcipher_export` vs app-level copy for vec0/FTS5, verify counts and KNN identity, time, rekey, Backup API under encryption |
| WAL/checkpoint policy | spec | A | db | `journal_size_limit`, `wal_autocheckpoint`, TRUNCATE in a quiet window from `atrium-run`; WAL grew to 180-800 MB with always-on readers |
| arm64 static build and benchmark | spike | B | shipping | musl cross-compile of SQLCipher+OpenSSL+sqlite-vec NEON, same bench on an arm64 host; `-march` flags per target |
| Upstream fix for `pkgsStatic.sqlcipher` | task | B | build | nixpkgs fails to build the static output; contribute the `--disable-shared` / `libsqlite3.a` fix or keep the override |
| Trim static OpenSSL | spike | B | build | Reduce the 5.7 MB libcrypto contribution (no-deprecated, minimal providers) without breaking SQLCipher |
| Host disk encryption on artemis | decision | B | ops | #23 candidate 9: LUKS or fscrypt on `/persist`, rollout and unlock (no `crypt` layer today) |
| Vector read path that does not depend on the page cache | research | B | search | Exact-cosine or ANN options that keep a warm index outside SQLite pages, since mmap is unavailable when encrypted |

## 10. Reproduction notes (throwaway, deleted from artemis)

* Build: `nix shell nixpkgs#go nixpkgs#pkgsStatic.stdenv.cc`, `CC=x86_64-unknown-linux-musl-gcc CGO_ENABLED=1`, `CGO_CFLAGS="-I<dir with sqlite3.h> -march=x86-64-v3 -DSQLITE_VEC_ENABLE_AVX -O2"`, plaintext variant: `go build -tags sqlite_fts5`; SQLCipher / SQLite3MC variants: `-tags "libsqlite3 sqlite_fts5"` with `CGO_LDFLAGS=-L<dir with libsqlite3.a> [-lcrypto]`; all with `-ldflags "-linkmode external -extldflags -static -s -w"`.
* Plaintext variant uses nixpkgs `sqlite.dev` for `sqlite3.h` (as #51), vec wrapper above, mattn's bundled SQLite.
* SQLCipher DSN: `file:x.db?_busy_timeout=30000&_txlock=immediate&_sqlcipher_key=%22x%27<64hex>%27%22`; SQLite3MC DSN: `file:x.db?...&hexkey=<64hex>` (cipher default chacha20; add `&cipher=sqlcipher&legacy=4` for compat).
* Sources: [SQLCipher design](https://www.zetetic.net/sqlcipher/design/), [SQLite3MC ChaCha20](https://utelle.github.io/SQLite3MultipleCiphers/docs/ciphers/cipher_chacha20/), [SQLite3MC AES-256](https://utelle.github.io/SQLite3MultipleCiphers/docs/ciphers/cipher_aes256cbc/), [sqlite-vec v0.1.9](https://github.com/asg017/sqlite-vec/releases/tag/v0.1.9), [mattn v1.14.52](https://github.com/mattn/go-sqlite3), [SQLite WAL](https://sqlite.org/wal.html).
