{
  lib,
  sqlcipher,
  openssl_3_5,
}: let
  libcrypto = openssl_3_5.overrideAttrs (old: {
    pname = "openssl-sqlcipher";
    doCheck = false;
    configureFlags = lib.subtractLists ["enable-ec_nistp_64_gcc_128" "enable-ktls"] old.configureFlags ++ ["no-bulk"];
    postInstall =
      (old.postInstall or "")
      + ''
        install -Dm444 LICENSE.txt $out/share/licenses/openssl.txt
      '';
  });
in
  (sqlcipher.override {openssl = libcrypto;}).overrideAttrs (finalAttrs: old: {
    pname = "sqlcipher-libsqlite3";
    buildFlags = ["libsqlite3.a"];
    installPhase = ''
      runHook preInstall
      mkdir -p $out/lib $out/include $out/share/licenses
      $AR -M <<MRI
      create $out/lib/libsqlite3.a
      addlib libsqlite3.a
      addlib ${libcrypto.out}/lib/libcrypto.a
      save
      end
      MRI
      cp sqlite3.h sqlite3ext.h $out/include/
      cp LICENSE.md $out/share/licenses/sqlcipher.md
      cp ${libcrypto.out}/share/licenses/openssl.txt $out/share/licenses/openssl.txt
      runHook postInstall
    '';
    postInstall = "";
    passthru =
      (old.passthru or {})
      // {
        openssl = libcrypto;
        appendNotices = file: let
          block = name: license: ''
            printf '================================================================\n${name}\n================================================================\n\n' >> ${file}
            cat ${finalAttrs.finalPackage}/share/licenses/${license} >> ${file}
            printf '\n' >> ${file}
          '';
        in
          block "SQLCipher ${finalAttrs.version} (BSD-3-Clause)" "sqlcipher.md"
          + block "OpenSSL ${libcrypto.version} (Apache-2.0)" "openssl.txt";
      };
  })
