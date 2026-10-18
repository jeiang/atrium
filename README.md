# atrium

atrium is one person's private assistant service, run on their own host. It
is meant as a unified agentic harness for a personal secretary, with basic
coding tooling on top. The harness does as much as it can in code, so the
agent does not spend tokens generating code to perform actions.

It keeps a local index of the operator's mail, calendar, contacts, Notion
wiki, spending and media library. Routine work runs in plain code with no
model in the loop: filing and flagging mail by recorded rules, trashing by
rule, extracting receipts into a ledger, tracking packages, filing torrents
and polling release feeds. A Claude-based agent sits on top for work that
needs judgment: answering questions from the operator's data, drafting
replies, keeping a wiki, weekly reviews, daily nudges and proposing actions.

Limits that hold today:

- The agent never sends mail. Only the operator's Send click does.
- Mail routing is code, not an agent decision.
- Every automatic write is logged with an undo.
- One operator per instance. More people means more instances.

## Status

The Python app in `src/atrium` runs today and is frozen to fixes.

v2 is a planned Go rewrite, in two phases:

- Phase A ports the app as-is: same behavior, same SQLite data, same host
  interface. Then it cuts over and the Python code is deleted.
- Phase B improves the Go code: OIDC login, settings editable in the UI and
  pinnable by deploy-time config, runtime-toggled modules, a remote microVM
  sandbox manager for scripts and repository work, GitHub integration, and
  model choice per chat.

The plan lives on the wayfinder map,
[issue #15](https://github.com/jeiang/atrium/issues/15), and the
[project board](https://github.com/users/jeiang/projects/4).

## Docs

- [docs/install.md](docs/install.md): add the NixOS module to a host.
- [docs/runbook.md](docs/runbook.md): environment and operating procedures.
- [docs/CONTRIBUTING.md](docs/CONTRIBUTING.md): dev shell, `just` recipes,
  pull requests and the wayfinder workflow.
- [AGENTS.md](AGENTS.md): invariants and rules for changes.

## License

MIT. See [LICENSE](LICENSE). Contributions need a DCO sign-off; see
[docs/CONTRIBUTING.md](docs/CONTRIBUTING.md).
