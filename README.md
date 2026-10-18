# atrium

atrium is a single-operator assistant that indexes iCloud and Gmail over
IMAP, files and flags mail by recorded rules, mirrors Notion, calendars and
contacts, runs a tool-using chat agent on the Anthropic API with scheduled
jobs, and files a media library from qBittorrent. It is a Python package
(`src/atrium`) with a FastAPI web UI, packaged and deployed through Nix.

## Using the module

```nix
{
  inputs.atrium.url = "github:jeiang/atrium";
  inputs.atrium.inputs.nixpkgs.follows = "nixpkgs";
}
```

```nix
{inputs, ...}: {
  imports = [inputs.atrium.nixosModules.default];

  services.atrium = {
    enable = true;
    mode = "live";
    environmentFiles = ["/run/secrets/atrium.env"];
    web.allowedHost = "atrium.example.org";
    publicUrl = "https://atrium.example.org";
    chatUrl = "http://127.0.0.1:8187/v1";
    embedUrl = "http://127.0.0.1:8188/v1";
  };
}
```

The module creates the static `atrium` user, `/var/lib/atrium`, the
`atrium-web`, `atrium-web-local`, `atrium-watch`, `atrium-agent-scheduler`,
`atrium-codemode` and timer units, and the `atrium-run` wrapper. The model
servers behind `chatUrl` and `embedUrl` (name their units in
`modelServerUnits`), the reverse proxy in front of the public listener, the
secrets in `environmentFiles` and persistence of the state directory are the
host's. `docs/runbook.md` lists the environment the units read and the
operating procedures.

`nix build .#atrium` builds the package and runs the tests; `just test` runs
them against the working tree in the devshell.
