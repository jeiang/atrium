# Operator decisions

Answers the operator gave during grilling. These are settled; do not reopen
them without new evidence. Newest round last.

## Round 1 (2026-11-15)

| # | Decision | Answer |
|---|---|---|
| D1 | Meaning of "web and mobile share the same interface" | The shared interface is the HTTP contract with the backend. The mobile app will be native. Web-wrapped and cross-platform UI toolkits are out of scope for mobile. |
| D2 | Where the static frontend is served from | Built separately, embedded in and served by the Go binary (one artifact, same origin). It must also run standalone against mocks. |
| D3 | Browser support | Latest two versions of Safari (iOS and macOS), Firefox and Chromium. Baseline Newly Available features are allowed when they degrade gracefully. |
| D4 | Devices in real use | iOS Safari (latest); Helium (Chromium based) on macOS; sometimes Firefox on NixOS. |
| D5 | Offline and latency | Online-first: instant cached shell, last-seen data readable offline, optimistic UI only for internal undoable actions. Not local-first. |
| D6 | Weight of "other people can use atrium" in feature-creep verdicts | Judge by the operator's own goals. Keep modularity so others can omit parts. Never keep a feature or provider only for hypothetical other operators. |
| D7 | Live usage evidence | SELECT-only `atrium db shell` sessions on artemis are allowed (each writes one `maintenance.sql` audit row). Counts only, no content copied. |
| D8 | Frontend technology constraints | None. Pick on evidence. TypeScript assumed. |
| D9 | Number of visual design directions | 4 structurally different directions, each on desktop and mobile. |
| D10 | Examples are not limits | Every list the operator gave is an example. Analysis must be comprehensive and go beyond the named items. |
| D11 | Scope of work now | Research, reports and prototypes only. No change to the application until plans, research and prototype choices are settled with the operator. |

## Taste input (D12, verbatim intent)

Likes:
- Claude and ChatGPT desktop apps, specifically the new-chat screen and the
  chat layout where tool cards are visible.
- The mail diagrams that show what went where. More visual components
  beyond text-only presentation.

Dislikes in the current atrium:
- Lots of small buttons.
- Much of the interface is the same size with no variation. Knowledge does
  not look like a knowledge base and the wiki does not look like a wiki;
  they are projections of the same thing and should be unified.
- Related parts are separated with no link between them (mail rules and
  mail decisions are separate apart from the "make rule" button).
- The Done tab is nice in theory but ends up visually overloading.

## Round 2 (2026-11-15), ids from research notes: `reports/00-open-questions.md`

The form's "reviewed" radio came back unset, but four answers were edited
by hand, so the answers below are treated as the operator's. Questions not
on the form keep the register's recommendation until the operator vetoes one.

| # | Register id | Decision |
|---|---|---|
| D13 | R1.2 | One Markdown folder is the only knowledge export. Notion leaves atrium entirely (mirror, wiki view, sink, token) after one last import. |
| D14 | R1.1 | Mail splits into a Mail app plus core signals, as recommended. Operator addition: "things like birthdays can be split out into a separate calendar app (that provides the calendar tooling)". A Calendar app is now a design question for workstream 6. |
| D15 | R1.11, R1.13 | Library owns followed shows, intake, filing and sweep. Torrents is a small download client usable alone. The torrent rule is restated by actor (`AGENTS.md:128` changes). |
| D16 | R1.9 | Server ops stays as one opt-in app: 5 worker-only tools, findings as `ops` pages, no push. |
| D17 | R1.4, R1.6, R1.7, R1.5, R1.8, R1.10 | Accepted: one inbox; one commitment object; facts under subject pages with the wiki as the default view; home is a composer with the day around it; apps switched on and off in the product by the operator only; Packages is its own opt-in app. |
| D18 | R1.3 | Approved: browser uses cookie-authenticated same-origin `/web/v1` from the same spec; `/api/v1` stays bearer-only. |
| D19 | R1.14 | Approved: remove phone action links; a push opens the signed-in card. |
| D20 | R1.15 | Approved: remove `db import-v2`. Operator: "restore was already tested and a backup exists". |
| D21 | R1.30, R1.31 | Approved: a migration may drop tables no code reads; remove the `modernc` driver test run. |
| D22 | R1.16 | NOT approved: proxy (forward-auth) sign-in stays. Reports that recommend its removal are overruled. |
| D23 | R1.20 | iOS work has not started. Remove the Swift client check until the iOS repository exists. |
| D24 | R1.18, R1.22, R1.28 | Accepted: static secrets on artemis live in the host file; bundle embeddings on CPU if the prototype passes its two gates; Gmail stays a separately synced mailbox. |
| D25 | R1.21 | NOT approved: the Google Calendar provider stays. |
| D26 | R1.40, R1.41 | The operator will run the read-only SQL session on artemis and will connect the browser relay extension. Neither has happened yet. |

## Round 3 (2026-11-15), final round on design and framework

The operator confirmed that he reviewed the form himself.

| # | Register id | Decision |
|---|---|---|
| D27 | R1.48, Q2.1 | Design base is A Workbench: its design system and its desktop structure. The phone interface comes from C Deck, integrated into A where appropriate. Weather sits on the home page (as B had it). C's diagrams are wanted, the knowledge map most of all. D's information density is liked; its color scheme and design language are not. B's serif type and its square look are rejected, and so is D's square look. C is "too big" for desktop use. |
| D28 | new | Palette: the browns, beiges and tans of A are the atrium look ("homey"). No blue: the operator first called A's blue "ok", then corrected it: "dont keep the blues, the browns are the colors i like". |
| D29 | new | Prototype defects to fix: A has missing padding (for example the modules settings page) and corners that are not rounded. In the prototype viewer every phone view is a bit too big and is cut off at the top, where the status bar would be. |
| D30 | R2.23, Q1.0 | Frontend framework: plain Svelte 5 (sv-router, TanStack Svelte Query). |
| D31 | R1.57, Q1.1 | The CSP may name the Svelte-created Trusted Types policy `svelte-trusted-html`. |
| D32 | R1.49, Q2.16 | Every send that cannot be undone gets a hold or an undo window: press and hold on the phone, a 10 second hold with Undo on desktop. |
| D33 | R2.7, R1.47, R1.12, R1.44, R1.42, R1.43, R1.45, R1.46 | Accepted as recommended: make the one-time API spec corrections now; proxy sign-in accepts identity headers only from trusted peers; one platform pause for all unattended work with Live and go-live in Mail; event candidates are ordinary inbox proposals and need Mail on; contacts stay in core and birthdays move to Calendar; birthdays become a derived calendar; the iCloud login is one shared connection; publish the Google OAuth app to Production with the calendar scope only. |
| D34 | next step | Build more prototypes based on the hybrid of D27 and D28 (still throwaway, in the research folder). No application change. |
| D35 | R1.40 | The operator ran the read-only SQL session on artemis and pasted the output. It is saved, with Notion page ids removed, as research notes: `04-feature-creep/usage-query-results-2026-11-15.txt`. The browser relay for live captures (R1.41) is still not connected. |

Not decided in round 3:

- R1.50 (the stricter touch-target rule). The operator asked for an explanation and asked whether A can take C's phone interface, because C is too big on desktop. Working default until he answers: the stricter rule applies to touch layouts only (the phone interface from C); the desktop keeps A's density.

## Round 4 (2026-11-15), the hybrids

The operator confirmed that he reviewed the form himself. Evidence:
research notes: `02-design/hybrid-evaluation.md` (section 9 for the state after the repair
passes) and `prototypes/design/h1` to `h3`.

| # | Register id | Decision |
|---|---|---|
| D36 | R1.71, Q2.19 | The base is h2 Ledger: A's design system and desktop shell, C's phone interface, the rebuilt Apps settings page, the one-row phone composer. |
| D37 | Q2.20, Q2.24 | The accent is moss green (as in h1), not umber or clay. The "ok" state keeps its own olive, clearly apart from moss (h1's repaired tokens), with the check icon and outlined tag as the second cue. Surfaces, lines and ink stay in the browns, beiges and tans of D28; no blue. |
| D38 | Q2.21 | Density: tables only for Inbox and Settings. Mail, Knowledge facts and Library keep A's cards and lists (as in h1), not h2's tables. |
| D39 | Q2.22 | Knowledge opens on a page. The subject map is one key or tap away. |
| D40 | Q2.23 | Home shows the compact weather card. The day timeline is an optional view. |
| D41 | R1.50, Q2.17 | The stricter touch rule is accepted for touch layouts only: every tappable control owns a 44 by 44 px square that overlaps no other. The desktop keeps its density. |
| D42 | P2.1, P2.3, Q2.15 | Accepted rules: the full exact change is visible before and during a hold to send; a send cannot be held until the end of the change has been on screen; an app that is switched off hides its asks and leaves every count; the one inbox is called "Inbox" and the phone tab stays "Decide". |
| D43 | new defect | "some of the elements did not have rounded borders": every visible surface, control, row group, table, image and diagram node must have a rounded corner. The evaluator had passed this "by eye"; the operator's observation overrules that. |
| D44 | next step | Turn the decisions into an implementation plan with issues. No application change yet. |
