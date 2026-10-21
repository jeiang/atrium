# Operator reference

atrium is a single-operator assistant: one Go binary with an embedded web UI and SQLite state, deployed through a NixOS module. The host supplies credentials, external services, the reverse proxy, persistence and backups.

This reference covers two things: how an instance is configured, and the behavior and boundaries an operator can rely on today.

## Configuration

- [Configuration reference](../config/reference.md) lists every setting with its type, default and class, and describes the TOML file, `<key>_file` secrets, `${NAME}` interpolation and precedence. [Settings and secrets](behavior.md#settings-and-secrets) summarizes how they behave.
- [Environment variables](../config/environment.md) lists every setting pinned through the environment; the database section of the behavior page covers [the database key](behavior.md#database).
- The same settings are described as a JSON Schema in [`atrium.schema.json`](../config/atrium.schema.json).
- [NixOS module options](nixos.md) lists the `services.atrium` options. They configure the deployment (units, listeners, deployment variables), not the settings registry.

The two config pages and the schema are generated from the settings registry and are not edited by hand.

## Current behavior

[Operator-visible behavior and boundaries](behavior.md) states what the running application does and refuses to do: [access](behavior.md#access), [mail safety](behavior.md#mail-safety), [assistant limits](behavior.md#assistant-limits), [settings and secrets](behavior.md#settings-and-secrets) and the [database](behavior.md#database).
