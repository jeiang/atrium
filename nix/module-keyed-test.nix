{
  pkgs,
  self,
}:
assert pkgs.lib.versionAtLeast pkgs.systemd.version "254";
  pkgs.testers.runNixOSTest {
    name = "atrium-keyed-unit";
    nodes.machine = {
      lib,
      config,
      ...
    }: {
      imports = [self.nixosModules.default];
      services.atrium = {
        enable = true;
        database.keyFile = "/run/atrium-test-key";
        web = {
          allowedHost = "127.0.0.1";
          host = "127.0.0.1";
        };
        host.core.ops_listen = "off";
      };
      systemd.services.atrium.wantedBy = lib.mkForce [];
      environment.systemPackages = [config.services.atrium.package pkgs.openssl pkgs.sqlite pkgs.curl];
      virtualisation.memorySize = 2048;
    };
    testScript = ''
      import shlex

      machine.start()
      machine.wait_for_unit("multi-user.target")
      machine.succeed("umask 077; openssl rand -hex 32 >/run/atrium-test-key")
      machine.succeed("mkdir -p /run/systemd/system/atrium.service.d")
      machine.succeed("printf '%s\\n' '[Service]' 'Environment=ATRIUM_DB_KEY_FILE=''${CREDENTIALS_DIRECTORY}/db-key' >/run/systemd/system/atrium.service.d/legacy.conf")
      machine.succeed("systemctl daemon-reload")
      machine.fail("systemctl start atrium.service")
      machine.wait_until_succeeds("test $(systemctl show -p NRestarts --value atrium.service) -ge 2", timeout=60)
      machine.wait_until_succeeds("test $(systemctl show -p ExecMainStatus --value atrium.service) = 1", timeout=30)
      machine.fail("systemctl is-active --quiet atrium.service")
      machine.succeed("journalctl -u atrium.service --no-pager | grep -F 'open ''${CREDENTIALS_DIRECTORY}/db-key'")
      machine.succeed("test ! -e /var/lib/atrium/atrium.db")
      machine.succeed("rm /run/systemd/system/atrium.service.d/legacy.conf; systemctl daemon-reload; systemctl reset-failed atrium.service; systemctl start atrium.service")
      machine.wait_for_unit("atrium.service")
      machine.succeed("curl -fsS http://127.0.0.1:8481/settings/auth >/dev/null")
      pid = machine.succeed("systemctl show -p MainPID --value atrium.service").strip()
      environment = machine.succeed(f"tr '\\0' '\\n' </proc/{pid}/environ").splitlines()
      credentials = next(line.split("=", 1)[1] for line in environment if line.startswith("CREDENTIALS_DIRECTORY="))
      assert "ATRIUM_DB_KEY_FILE=" + credentials + "/db-key" in environment, environment
      machine.succeed("systemctl stop atrium.service")
      machine.succeed("test -s /var/lib/atrium/atrium.db")
      machine.succeed("test \"$(head -c 15 /var/lib/atrium/atrium.db)\" != 'SQLite format 3'")
      machine.fail("sqlite3 /var/lib/atrium/atrium.db 'SELECT count(*) FROM sqlite_master'")
      unkeyed_environment = " ".join(shlex.quote(line) for line in environment if line.startswith("ATRIUM_") and not line.startswith("ATRIUM_DB_KEY_FILE="))
      machine.fail("env -u ATRIUM_DB_KEY_FILE -u ATRIUM_DB_KEY -u CREDENTIALS_DIRECTORY " + unkeyed_environment + " atrium db check >/tmp/unkeyed.out 2>/tmp/unkeyed.err")
      refusal = machine.succeed("cat /tmp/unkeyed.err")
      assert "database file is encrypted but no key is configured" in refusal, refusal
      machine.succeed("atrium-run db check | grep -F 'encryption: encrypted'")
      value = "literal-''${HOME}-$$-$X"
      output = machine.fail("atrium-run settings get " + shlex.quote(value) + " 2>&1")
      assert value in output, output
      machine.succeed("mv /run/atrium-test-key /run/atrium-test-key.saved")
      machine.fail("atrium-run db check")
    '';
  }
