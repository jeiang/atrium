{lib, ...}: {
  perSystem = {
    pkgs,
    self',
    ...
  }: {
    checks = lib.optionalAttrs (self'.packages ? atrium) {
      restore-drill =
        pkgs.runCommand "atrium-restore-drill" {
          nativeBuildInputs = [self'.packages.atrium pkgs.coreutils];
          golden = ../testdata/golden/atrium.db;
        } ''
          set -euo pipefail
          export HOME=$TMPDIR
          unset ATRIUM_CONFIG CREDENTIALS_DIRECTORY ATRIUM_DB_KEY ATRIUM_DB_KEY_FILE
          key=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')

          corrupt() {
            cp "$1" "$2"
            chmod u+w "$2"
            size=$(stat -c %s "$2")
            printf '\377\377\377\377' | dd of="$2" bs=1 seek=$((size / 2)) conv=notrunc status=none
          }

          drill() {
            name=$1
            state=$TMPDIR/$name
            mkdir -p "$state/files/threads/1" "$TMPDIR/$name-out"
            printf 'drill file' >"$state/files/threads/1/note.txt"
            export ATRIUM_DB=$state/atrium.db ATRIUM_CACHE_DIR=$state/cache
            "$2"
            atrium backup "$TMPDIR/$name-out/atrium.backup"
            atrium restore --verify "$TMPDIR/$name-out/atrium.backup"
            if atrium backup "$TMPDIR/$name-out/atrium.backup"; then
              echo "backup overwrote an existing archive" >&2
              exit 1
            fi
            corrupt "$TMPDIR/$name-out/atrium.backup" "$TMPDIR/$name-out/corrupt.backup"
            if atrium restore --verify "$TMPDIR/$name-out/corrupt.backup"; then
              echo "verify accepted a corrupted archive" >&2
              exit 1
            fi
          }

          plain() {
            install -m 0640 "$golden" "$ATRIUM_DB"
          }
          drill plain plain

          keyed() {
            export ATRIUM_DB_KEY=$key
            atrium settings list >/dev/null
            atrium db shell <<'SQL'
          INSERT INTO notion_pages (rid, id, title, path, url, markdown) VALUES (1, 'p1', 'Alpha page', 'Root', 'https://notion.example/p1', 'alpha beta gamma');
          INSERT INTO notion_fts (rowid, title, path, body) VALUES (1, 'Alpha page', 'Root', 'alpha beta gamma');
          SQL
          }
          drill keyed keyed

          wrong=$(head -c 32 /dev/urandom | od -An -tx1 | tr -d ' \n')
          if ATRIUM_DB_KEY=$wrong atrium restore --verify "$TMPDIR/keyed-out/atrium.backup"; then
            echo "verify accepted the wrong key" >&2
            exit 1
          fi
          if env -u ATRIUM_DB_KEY atrium restore --verify "$TMPDIR/keyed-out/atrium.backup"; then
            echo "verify accepted a keyed archive without a key" >&2
            exit 1
          fi
          touch $out
        '';
    };
  };
}
