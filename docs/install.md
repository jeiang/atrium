# Install

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
inert `atrium-codemode` and timer units, and the `atrium-run` wrapper. The model
servers behind `chatUrl` and `embedUrl` (name their units in
`modelServerUnits`), the reverse proxy in front of the public listener, the
secrets in `environmentFiles` and persistence of the state directory are the
host's. [runbook.md](runbook.md) lists the environment the units read and the
operating procedures.

`services.atrium.package` defaults, in the flake's `nixosModules.default`, to the Go build (`nix build .#atrium-go`), which embeds the assets built by `atrium-static`; `staticPackage` and `ATRIUM_STATIC_DIR` are gone. `atrium-notion-sync` runs `atrium notion sync` on its timer, and `atrium-codemode` is inert. `nix build .#atrium` builds the Python package and runs its tests; `just test` runs
them against the working tree in the devshell.
