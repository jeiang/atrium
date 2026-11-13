def fail($ok; $message): if $ok then . else error($message) end;
def declarations: map("--\(.name):\(.value);") | join("");
. as $t
| ([.color.tokens[], .spacing.tokens[], .radius.tokens[], .shadow.tokens[], .layout.tokens[]] | map(.name)) as $names
| fail(.version == 1; "unsupported tokens version")
| fail(($names | unique | length) == ($names | length); "duplicate token name")
| fail(all($names[]; test("^[a-z][a-z0-9-]*$")); "unsafe token name")
| fail(all(.color.tokens[]; (.value.light | type == "string") and (.value.dark | type == "string")); "colors require light and dark values")
| fail(all(.type.groups[]; .family as $f | $t.type.families | has($f)); "unknown font family")
| fail(all(.type.fonts[]; .family as $f | any($t.type.families[]; contains($f))); "unknown font reference")
| (.layout.tokens | map({key:.name,value:.value}) | from_entries) as $layout
| fail($layout["touch-min"] == "44px" and $layout["reading-w"] == "760px" and $layout["panel-w"] == "320px" and $layout["bp-mobile"] == "768px" and $layout["bp-panel"] == "1024px"; "required layout tokens missing or invalid")
| ([.color.tokens[] | {name, value:.value.light}] + .spacing.tokens + .radius.tokens + .shadow.tokens + .layout.tokens | declarations) as $root
| ([.color.tokens[] | {name,value:.value.dark}] | declarations) as $dark
| (.type.families | to_entries | map("--family-\(.key):\(.value);") | join("")) as $fonts
| ([.type.groups[] | .family as $family | .styles[] | "--type-\(.name)-family:var(--family-\($family));--type-\(.name)-size:\(.fontSize);--type-\(.name)-leading:\(.lineHeight);--type-\(.name)-weight:\(.fontWeight);--type-\(.name)-tracking:\(.letterSpacing // "normal");--type-\(.name)-style:\(.fontStyle // "normal");"] | join("")) as $type
| ":root{color-scheme:light;\($root)\($fonts)\($type)}\n[data-theme=dark]{color-scheme:dark;\($dark)}\n@media(prefers-color-scheme:dark){[data-theme=system]{color-scheme:dark;\($dark)}}\n@media(width < \($layout["bp-mobile"])){button,[role=button],a.hub-tab,.menu-item{min-height:var(--touch-min)}.ask-send{min-width:var(--touch-min);min-height:var(--touch-min)}.composer textarea{font-size:16px}}\n@media(width >= \($layout["bp-panel"])){.workspace-panel{width:var(--panel-w)}}"
