# Template port prototype (#75), throwaway

Run: `python3 prototypes/templates/jinja/gen_data.py`, `nix develop --impure -c python prototypes/templates/jinja/render.py`,
then `go run ./gotmpl ./templ ./gonja` (each separately) from this directory and `python3 diff.py out/jinja-thread.html out/<dir>-thread.html`.
Per-approach findings: `gotmpl/NOTES.md`, `templ/NOTES.md`, `gonja/NOTES.md`. Comparison and recommendation are in the comment on issue #75.
