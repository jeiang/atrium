GENERIC_INPUTS = [
    "",
    "Ünïcödé café naïve ß ÉCOLE İstanbul ǅ x_y-z 中文 ٣٤٥ ０９ ²³ ① \u00a0nbsp\u2003em\u2028sep",
    "line one\nline two\r\nline three\n",
    "emoji 😀 x\U0001f600y 👍🏽 👨\u200d👩\u200d👧 𝒜𝓁𝓅𝒽𝒶 𐐷 𝟘𝟙 2026 😀\n😀",
]

GENERIC_BYTES_INPUTS = [
    b"",
    b"caf\xe9 na\xefve \xb2\xb3 \xa0nbsp x_y-z \xdf",
    b"line one\nline two\r\nline three\n",
]

USER_PATTERNS = [
    "^lanterns s01e01",
    "1080p",
    "720p",
    r"^claude\b",
    r"\balert\b",
    "^alert",
    "bill",
    "(",
    "",
    r"(?P<ep>s\d{2}e\d{2})",
    r"x26[45]|hevc",
    r"\d{4}$",
    "café",
    r"(?<=s)\d+",
    r"a{,3}b",
    r"\bnow\Z",
    r"(?i)Amazon",
    r"[[:alpha:]]+",
    r"(\w)\1",
    r"\p{L}",
    r"(?<!un)paid\b",
    r"^(?:AMZN|AMAZON)\s+MKTP",
]

USER_INPUTS = [
    "Lanterns S01E01 1080p WEB H264-GRP",
    "lanterns s01e01 720p x265 HEVC",
    "Lanterns.S01E02.2160p.WEB.x264",
    "CLAUDE.AI SUBSCRIPTION",
    "ANTHROPIC CLAUDE",
    "Security ALERT now",
    "an alert",
    "Your bill is ready",
    "AMAZON MKTPLACE PMTS",
    "AMZN MKTP US*2K4",
    "Café Ünïcödé 2026",
    "Lanterns 😀 S01E01 1080p 𝒜lert",
    "aab aaaab b",
    "unpaid invoice, paid in full",
    "Season ends now\n",
    "book keeper ٣٤٥٦",
]


def _user(name, flagged):
    out = {}
    for i, pattern in enumerate(USER_PATTERNS):
        if flagged:
            out[f"{i:02d}"] = {"pattern": pattern, "case_sensitive": False}
            out[f"{i:02d}-cs"] = {"pattern": pattern, "case_sensitive": True}
        else:
            out[f"{i:02d}"] = {name: pattern}
    return out


BINDINGS = {
    "src/atrium/imap.py:_section:a403c216": {key: {"key": key} for key in ("FLAGS", "X-GM-LABELS")},
    "src/atrium/imap.py:_section:a4704e8d": {key: {"key": key} for key in ("FLAGS", "X-GM-LABELS")},
    "src/atrium/imap.py:parse_status:b6ef0e5c": {key: {"key": key} for key in ("UIDVALIDITY", "UIDNEXT", "HIGHESTMODSEQ", "MESSAGES")},
    "src/atrium/media/channels.py:compile_pattern:38c283f4": _user("pattern", True),
    "src/atrium/money.py:match_alias:b4261d17": {label: {"r": {"pattern": v["value"]}} for label, v in _user("value", False).items()},
    "src/atrium/money.py:add_alias:b686849e": _user("pattern", False),
    "src/atrium/rules.py:condition_matches:afff542b": _user("value", False),
    "src/atrium/rules.py:validate_condition:9fc389e6": _user("value", False),
}

FETCH_META = (
    '1 (UID 42 FLAGS (\\Seen $Junk) X-GM-LABELS ("\\\\Inbox" "Receipts (old)") MODSEQ (991) '
    'RFC822.SIZE 2048 X-GM-MSGID 1700000000000000001 X-GM-THRID 1700000000000000002 '
    'INTERNALDATE "17-Oct-2026 09:15:00 -0400" BODYSTRUCTURE ("text" "plain" NIL NIL NIL "7bit" 12 1) BODY[HEADER] {120}'
)
STATUS_LINE = '"INBOX" (MESSAGES 231 UIDNEXT 4412 UIDVALIDITY 1 HIGHESTMODSEQ 99812)'
MARKDOWN = (
    "# Title\n\nSome `inline` code and **bold** __under__ ~~strike~~ text.\n\n"
    "```python\nprint('<page url=\"x\">')\n```\n\n"
    "- [ ] task one\n* [x] done\n+ plain\n12. numbered\n> quote\n>no space\n---\n***\n"
    "![alt text](https://img.test/a.png) and [link](https://a.test/b \"t\") and \\| pipe | cell\n"
    "<empty-block/>\n  <empty-block />  \n"
)

INPUTS = {
    "src/atrium/agent/files.py:PAGE": [
        b"<< /Type /Page /Parent 2 0 R >>",
        b"<< /Type/Pages /Kids [3 0 R] >>",
        b"/Type /Page/Contents 4 0 R /Type  /PageLabel /Type\n/Page>>",
        b"/Type /Page_1 /Type /Page\xe9",
    ],
    "src/atrium/agent/files.py:safe_name:e8618e2d": ["report final (v2).pdf", "Résumé 2026 ✓.docx", "../../up two levels.txt", "a__b..c--d", "party 🎉 notes 😀.md"],
    "src/atrium/agent/scheduler.py:TIME": ["07:30", "7:05", "123:00", "07:3", "07:30\n", "٠٧:٣٠"],
    "src/atrium/agent/skills.py:NAME": ["notion-wiki", "a", "Bad", "x" * 48, "x" * 49, "-lead", "ok_name\n", "naïve"],
    "src/atrium/agent/skills.py:REF": [
        "use /skill:notion-wiki now",
        "path/skill:nope and [[skill:money_rules]]",
        "a/skill:x b /skill:Upper [[skill:]] /skill:-bad",
        "é/skill:after-letter",
    ],
    "src/atrium/agent/subagents.py:NAME": ["researcher", "r" * 32, "r" * 33, "Has Space", "a-b_c9"],
    "src/atrium/agent/threads.py:URL": [
        "see https://a.test/x?y=1&z=2) and <http://b.test/path>.",
        "quoted \"https://c.test/q\" [https://d.test/e]",
        "http:// bare and https://ünï.test/päth",
    ],
    "src/atrium/agent/tools/notify.py:THREAD_LINK": ["/chat/42", "/chat/", "/chat/42/x", "/chat/٤٢", "/chat/42\n"],
    "src/atrium/agent/tools/notify.py:CONTROL": ["ok\ttext\nline", "bell\x07 vt\x0b ff\x0c esc\x1b del\x7f", "\x00\x08\x0e\x1f"],
    "src/atrium/agent/tools/notion_write.py:MEDIA_SRC": [
        '<img src="https://a.test/x.png" alt="x"/>',
        '<IMAGE\n  width="3" SRC="file.png">',
        '\\<img src="escaped.png"> <iframe class="c" src="https://e.test/">',
        '<imgx src="no.png"> <video data-src="a" src="b.mp4">',
    ],
    "src/atrium/agent/tools/notion_write.py:URL": ["https://a.test", "HTTP://A.TEST", "file.png", "git+ssh://host/repo", "1http://x", "mailto:x@y.test"],
    "src/atrium/contacts.py:ESCAPE": ["a\\nb\\Nc\\,d\\;e\\\\f", "trailing\\", "\\x not escaped"],
    "src/atrium/contacts.py:SPLIT": ["Family;Given;;Mr.;", "Smith\\;Jones;Ann", "\\\\;x"],
    "src/atrium/contacts.py:DATE": ["1990-04-17", "19900417", "--04-17", "--0417", "1604-12-25", "2026-02-301", "1990-4-17", "١٩٩٠-٠٤-١٧"],
    "src/atrium/contacts.py:APPLE_LABEL": ["_$!<Anniversary>!$_", "_$!<Other>!$_ ", "_$!<a>b>!$_", "Home"],
    "src/atrium/drafts.py:_clean_line:72b191b6": ["plain subject", "inject\r\nBcc: x", "line\nfeed", "u\u2028sep"],
    "src/atrium/drafts.py:send:b50aef46": ["<abc.123@mail.test>", " <x y>\t", "noangles@host"],
    "src/atrium/features.py:CURRENCY": [
        "Total: $1,234.56 due",
        "USD 99.00 and ttd1,000 and TT$ 50",
        "Charged 12.50 USD, 7.25cad, €3.50, £10.00",
        "usd1234 cadence 1.5usd $12",
        "US$ 5.00 tt$20.00",
    ],
    "src/atrium/features.py:OTP": [
        "Your code is 123456.",
        "ref #123456 or $123456 or 1234567",
        "-123456 123456- 12345 654321",
        "code:987654\nnext 1.123456",
        "٣٤٥٦٧٨ is not ascii",
    ],
    "src/atrium/features.py:VCODE": ["Your verification code", "one-time passcode", "One Time code", "your code login code", "security codes"],
    "src/atrium/features.py:REPLY_PREFIX": ["Re: hello", "  FWD : x", "fw:y", "AW: Re: z", "Reply: no", "re:"],
    "src/atrium/features.py:subject_template:406a98ac": ["paid $1,234.56 today", "usd 99 and 12.5 ttd", "tt$5 us$7 cad3", "€ 4,000"],
    "src/atrium/features.py:subject_template:7e7d9d58": ["meeting on jan 5th, 2026", "september 30 2025 and sept. 1", "may 12 maybe", "decimal 3"],
    "src/atrium/features.py:subject_template:cf4d1562": ["on 5th jan, 2026", "30 september 2025", "1 sept. 26", "12 mayday"],
    "src/atrium/features.py:subject_template:adeb272e": ["due 2026/10/18 or 18-10-2026", "1/2/3 and 12345/1/1", "v1.2/3"],
    "src/atrium/features.py:subject_template:b52642fe": ["order a1b2 and 123 and abc", "x_9 é3 ٣", "<amt> stays", "😀 a1😀 𝒜1 𝟘x"],
    "src/atrium/features.py:subject_template:4c87e833": ["invoice # <n> and #<n>", "#  <n>x"],
    "src/atrium/features.py:subject_template:088c52e8": ["a  b\t\tc\n\nd", " lead and trail ", "nb\u00a0sp"],
    "src/atrium/features.py:strip_markup:789e1f9c": [
        "<p>hi</p><script>alert(1)</script><STYLE>x{}</style>end",
        "<script>a</style>b</script>",
        "<Script type=x>\nmulti\nline\n</SCRIPT> tail",
    ],
    "src/atrium/features.py:strip_markup:8b65452d": ["<p>a<br/>b</p>", "a < b > c", "<a href='x'>link</a>"],
    "src/atrium/features.py:strip_markup:088c52e8": ["a &amp;\n\n b", "\u00a0x\u2003y"],
    "src/atrium/features.py:filename_pattern:03bf5b4f": ["newsletter-2026-10", "Shop 24/7", "no digits"],
    "src/atrium/features.py:body_text:088c52e8": ["x \t y\nz", "single"],
    "src/atrium/features.py:normalize_msgid:b50aef46": ["<a.b@c.test>", " <x>\n"],
    "src/atrium/features.py:msgid_list:4262beb1": ["<a@b.test> <c d@e> <mailto:x@y>, <>", "<one><two>"],
    "src/atrium/features.py:extract:a0ed98d1": [
        "v=1; a=rsa-sha256; d=mail.example.com; s=sel",
        "d=first;d=second",
        "dd=nope; xd=no; d=ok.test",
    ],
    "src/atrium/flags.py:AUTOMATED_LOCAL": ["noreply", "no-reply", "Do.Not_Reply", "notifications", "mailer-daemon", "bounce", "bounces+x", "john"],
    "src/atrium/flags.py:unfold:ceb3ae39": ["Subject: a\r\n b\n\tc", "no fold\nhere"],
    "src/atrium/imap.py:LIST_LINE": [
        b'(\\HasNoChildren \\Sent) "/" "Sent Messages"',
        b'(\\Noselect) NIL Archive',
        b'() "\\\\" "Quoted \\"name\\""',
        b'(\\Marked) "." INBOX\r\n',
    ],
    "src/atrium/imap.py:TOKEN": ['(\\Seen "a \\"b\\"" NIL)', '("\\\\Inbox" Receipts)', "plain"],
    "src/atrium/imap.py:utf7_decode:967eaba6": ["Entw&APw-rfe", "&AOQA9gD8-", "no shift", "a&-b"],
    "src/atrium/imap.py:parse_copyuid:ac698e2b": ["[COPYUID 38505 304,319:320 3956:3958] Done", "COPYUID 1 2 3", "COPYUID x 1 2"],
    "src/atrium/imap.py:_unquote:3d0d7e03": ['a\\"b\\\\c', "\\", "plain"],
    "src/atrium/imap.py:parse_labels:a98611ec": ['"a b" c "d\\"e" f', "single", '"unterminated x'],
    "src/atrium/imap.py:_section:a403c216": [FETCH_META, "XFLAGS (a) FLAGS(b)", "X-GM-LABELS ()"],
    "src/atrium/imap.py:structure_attachments:8d10bc65": [FETCH_META, "BODYSTRUCTURE"],
    "src/atrium/imap.py:parse_fetch:5f599446": ["1 (UID 3", "  12 (", "* 1 FETCH (", "1  ("],
    "src/atrium/imap.py:parse_fetch:94999554": [FETCH_META, "BODY[TEXT] {5}", "BODY[MIME]"],
    "src/atrium/imap.py:parse_fetch:5f599446#2": ["1 (UID 3", "  12 (", "x 1 ("],
    "src/atrium/imap.py:parse_fetch:de5a295a": [FETCH_META, "XUID 5 UID 6", "UID x"],
    "src/atrium/imap.py:_fetched:b7777f95": [FETCH_META, "MODSEQ (x)"],
    "src/atrium/imap.py:_fetched:41ca0e1d": [FETCH_META, "RFC822xSIZE 5"],
    "src/atrium/imap.py:_fetched:b296c151": [FETCH_META],
    "src/atrium/imap.py:_fetched:c4bf6001": [FETCH_META],
    "src/atrium/imap.py:_fetched:34f2c716": [FETCH_META, 'INTERNALDATE ""'],
    "src/atrium/imap.py:_fetched:de5a295a": [FETCH_META, "UID 7"],
    "src/atrium/imap.py:parse_status:b6ef0e5c": [STATUS_LINE, "MESSAGES x UIDNEXT 5", "XUIDNEXT 9"],
    "src/atrium/media/eztv.py:IMDB_RE": ["tt1234567", "1234567", "tt12345678901", "tt123456", "TT1234567", "tt1234567\n"],
    "src/atrium/media/library.py:SEASON_DIR": ["Season 1", "Season 012", "Season 1000", "season 1", "Season ٣"],
    "src/atrium/media/library.py:EPISODE_MARK": ["Show - S01E02", "Show - S01E123", "Show - S1E02", "Show - S01E02.mkv"],
    "src/atrium/media/parse.py:EPISODE_RE": ["Show.S01E02.1080p", "show s1 e3", "Show.S01E02E03", "xS01E02", "Show_S01E02", "Show.S٠١E٠٢", "Show.ſ01E02"],
    "src/atrium/media/parse.py:YEAR_RE": ["Movie.2019.1080p", "Movie (1999)", "x2019", "2019x", "1899 2100", "é2019é"],
    "src/atrium/media/parse.py:LEAD_TAG_RE": ["[GRP] Show - 01", "  [a][b] x", "[unclosed x", "\u00a0[GRP] x"],
    "src/atrium/media/parse.py:SAMPLE_RE": ["Movie.SAMPLE.mkv", "samples", "sample_1", "Sample-é"],
    "src/atrium/media/qbit.py:BTIH": [
        "urn:btih:" + "a" * 40,
        "urn:btih:" + "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567",
        "urn:btih:" + "a" * 39,
        "urn:btih:" + "abcdefghijklmnopqrstuvwxyz234567",
        "URN:btih:" + "0" * 40,
    ],
    "src/atrium/money.py:_patterns:875c02cb": ["1234, 5678 9012", ",lead,,trail ", "single"],
    "src/atrium/money.py:add_account:78701180": ["**** 1234", "x٣4"],
    "src/atrium/money.py:update_account:78701180": ["ending 4321", ""],
    "src/atrium/money.py:parse_rate_table:d072aa40": ["2026-10-18", "2026-1-18", "2026-10-18\n", "٢٠٢٦-١٠-١٨"],
    "src/atrium/money.py:STORE_NUMBER": ["AMAZON #123", "STORE 1234 X 12", "#  9 ab 99 999"],
    "src/atrium/money.py:normalize_raw:06157e46": ["AMZN Mktp US*2K4!", "JOHN'S CAFÉ & CO. - 7", "ÜBER"],
    "src/atrium/money.py:_currency:8a6b6517": ["USD", "usd", "USDX", "TT$", "ÜSD"],
    "src/atrium/money.py:clean_extraction:78701180": ["acct 0042", "none"],
    "src/atrium/money.py:period_bounds:53692041": ["2026-10", "2026-1", "2026-10\n"],
    "src/atrium/money.py:period_bounds:98811e5e": ["2026", "20266", "٢٠٢٦"],
    "src/atrium/money.py:period_bounds:23691e9c": ["30d", "d", "30D", "1.5d"],
    "src/atrium/money.py:period_bounds:46dc6db4": ["2026-01-01/2026-01-31", "2026-01-01 / 2026-01-31", "2026-01-01/"],
    "src/atrium/notion_api.py:UUID_HEX": ["0123456789abcdef0123456789ABCDEF", "page-0123456789abcdef0123456789abcdef-x", "0123456789abcdef"],
    "src/atrium/notion_api.py:DASHED": ["01234567-89ab-cdef-0123-456789abcdef", "01234567-89ab-cdef-0123-456789abcde", "x01234567-89AB-CDEF-0123-456789ABCDEFx"],
    "src/atrium/notion_api.py:ids_in_text:f41d7d52": [
        "https://www.notion.so/Page-0123456789abcdef0123456789abcdef?pvs=4",
        "ids 01234567-89ab-cdef-0123-456789abcdef and 0123456789ab-cdef01234567-89abcdef",
        "short abcdef",
    ],
    "src/atrium/notion_markdown.py:CODE": [MARKDOWN, "a `b` c `` d `e\nf`", "  ```\nunclosed\n", "```js\nx\n  ```  \nafter"],
    "src/atrium/notion_markdown.py:ATTR": ['<page url="https://a.test" data-id="x"> naïve="é" =""', 'a="b" c="unterminated'],
    "src/atrium/notion_markdown.py:REF": [
        '<page url="https://n.test/1">Title</page> and <mention-page url="u"/>',
        '\\<database url="x">esc</database> <mention-data-source url="d">Src\nline</mention-data-source>',
        '<page url="a">x</database> <pages url="b"/>',
    ],
    "src/atrium/notion_markdown.py:MEDIA": ['<audio src="a.mp3"/> <video src="v">cap\ntion</video>', '\\<file src="f"/> <pdf src="p">x</file>', "<embedx/>"],
    "src/atrium/notion_markdown.py:DATE": ['<mention-date start="2026-10-18"/>', '\\<mention-date start="x"/>', "<mention-date>"],
    "src/atrium/notion_markdown.py:TABLE": [
        '<table fit-page-width="true">\n<tr><td>a</td><th>b</th></tr>\n</table>',
        "\\<table>x</table> <tablex>y</table>",
    ],
    "src/atrium/notion_markdown.py:ROW": ['<tr><td>a</td></tr>\n<tr color="red">\n<td>b</td>\n</tr>', "<trx>no</tr>"],
    "src/atrium/notion_markdown.py:CELL": ['<td>a</td><th colspan="2">b\nc</th><td>d</th>', "<tdx>no</td>"],
    "src/atrium/notion_markdown.py:EMPTY": [MARKDOWN, "x <empty-block/>"],
    "src/atrium/notion_markdown.py:BREAK": ["a<br>b<br/>c<br />d\\<br>", "<brx>"],
    "src/atrium/notion_markdown.py:SUMMARY": ["<details><summary>Sum\nmary</summary>body</details>", "\\<summary>x</summary>"],
    "src/atrium/notion_markdown.py:WRAPPER": [
        '<details color="gray"><callout icon="x">text</callout></details>',
        "<columns><column>a</column></columns> <span>b</span> <synced_block_reference url=\"u\"/>",
        "<colx> <col> </col> \\<span> <mention-user url=\"u\"/>",
    ],
    "src/atrium/notion_markdown.py:BLOCK_ATTRS": ['Heading {color="blue"}', 'Para {toggle="true" color="red_bg"}  \nnext', "{} {bad}"],
    "src/atrium/notion_markdown.py:IMAGE": [MARKDOWN, "![a](b c)", "![](x)"],
    "src/atrium/notion_markdown.py:UNESCAPED_PIPE": ["a | b \\| c|d", "|"],
    "src/atrium/notion_markdown.py:LINK": [MARKDOWN, "[a]() [b](c (d))"],
    "src/atrium/notion_markdown.py:HEADING": [MARKDOWN, "####### seven\n#nospace\n  ## indented"],
    "src/atrium/notion_markdown.py:LIST_MARK": [MARKDOWN, "-no space\n  - [X] upper"],
    "src/atrium/notion_markdown.py:QUOTE": [MARKDOWN],
    "src/atrium/notion_markdown.py:RULE": [MARKDOWN, "--\n- - -\n  ----  "],
    "src/atrium/notion_markdown.py:EMPHASIS": [MARKDOWN],
    "src/atrium/notion_markdown.py:TAG": ['<page url="x">t</page> <br/> \\<span> <a-b_c x="1"> <1abc> <Upper>', "<x y<z>"],
    "src/atrium/notion_markdown.py:CUT_TAG": ["text <page url=", "a < b", "done <b>", "\\<x", "</unfinished"],
    "src/atrium/notion_markdown.py:ESCAPE": ["\\* \\_ \\# \\| \\\\ \\a \\$ \\^ \\~ \\<"],
    "src/atrium/notion_proposals.py:HEADING": ["## Related ##", "# Title", "#NoSpace", "######## too many", "### trailing   #  "],
    "src/atrium/notion_proposals.py:LIST_ITEM": ["- item", "  * item", "12. item", "-item", "1) item", "+\titem"],
    "src/atrium/notion_wiki.py:FOOTER": [
        "Last reviewed 2026-10-18 · sources: a, b",
        "Last reviewed 2026-10-18",
        "Last reviewed 2026-10-18 · sources:  ",
        "last reviewed 2026-10-18",
        "Last reviewed 2026-10-18 - sources: x",
    ],
    "src/atrium/notion_wiki.py:LINK": ["[a](https://x.test) [b](c d) [](e)", "[x]( y)"],
    "src/atrium/notion_wiki.py:HEADING": ["## Heading ##", "#x", "### Spaced   "],
    "src/atrium/notion_wiki.py:URL": ["https://x.test", "notion://x", "/relative", "C:\\path"],
    "src/atrium/notion_wiki.py:ALIAS_SPLIT": ["a, b; c\nd", "none"],
    "src/atrium/notion_wiki.py:link_label:7c506ee0": ["https://www.example.test/x", "HTTP://WWW.X.test", "ftp://host", "www.x.test"],
    "src/atrium/openloops.py:norm_subject:c0f7446b": ["Re: Fwd: hello", "  FW : x", "sv:aw: y", "Regarding: z"],
    "src/atrium/openloops.py:norm_subject:088c52e8": ["a  b\t\nc", "\u00a0x"],
    "src/atrium/rebalance.py:terms_of:8772f9a4": ["Your {order} from Amazon #123", "ab cd éèé ÉCOLE"],
    "src/atrium/rebalance.py:clean_name:575b7ea7": ["Bills/2026|Q4\\x\x01y", "plain"],
    "src/atrium/rebalance.py:clean_name:088c52e8": ["a   b\t c"],
    "src/atrium/report.py:SINCE": ["24h", "7d", "1w", "h", "07d", "٧d"],
    "src/atrium/search.py:TOKEN": ['find "exact phrase" and words_with-dash', 'unterminated "quote', "café ٣٤ 中文"],
    "src/atrium/search.py:WORD": ["hello_world foo-bar", "Ünïcödé ٣", "___", "😀hi𝒜b😀 𐐷"],
    "src/atrium/smtp.py:split_addresses:3d7bad05": ["a@example.test,, ,b@example.test", " , a@example.test ,\r\n ", '"B, c" <b@x.test>,a@x.test', "plain", ""],
    "src/atrium/watches.py:merchant_key:618a0867": ["Amazon.com, Inc.", "Café Ünï", ""],
    "src/atrium/watches.py:UPS_RE": ["Tracking 1Z999AA10123456784 now", "1Z999AA1012345678", "x1Z999AA10123456784"],
    "src/atrium/watches.py:S10_RE": ["Parcel RA123456789US arrives", "ra123456789us", "RA123456789USA"],
    "src/atrium/watches.py:USPS_RE": ["USPS 9400111899223197428490", "92001234567890123456", "9612345678901234567890"],
    "src/atrium/watches.py:DHL_JD_RE": ["DHL JD014600006281187141", "JD01460000628118714"],
    "src/atrium/watches.py:FEDEX_RE": ["FedEx 123456789012 and 123456789012345", "1234567890123", "٠١٢٣٤٥٦٧٨٩٠١"],
    "src/atrium/watches.py:DHL_RE": ["DHL 1234567890 or 12345678901", "123456789"],
    "src/atrium/watches.py:ORDER_RE": [
        "Your order #112-1234567-1234567 has shipped",
        "Order number: AB-12345",
        "order no. 98765 and ORDER ID: x1",
        "Reorder 12345 border abcde",
        "order reference ref12345",
    ],
    "src/atrium/watches.py:<module>:c92625d2": ["Out for delivery today", "out  for delivery"],
    "src/atrium/watches.py:<module>:39489f00": [
        "Your package was delivered",
        "It will be delivered tomorrow",
        "Expected to be delivered",
        "Can be delivered; DELIVERED!",
        "undelivered",
    ],
    "src/atrium/watches.py:<module>:4e8ce3b3": ["Delivery attempt failed", "Returned to Sender", "address issues"],
    "src/atrium/watches.py:<module>:cd61c20e": ["Delayed shipment", "delay", "delays", "Running late"],
    "src/atrium/watches.py:<module>:fb0585ba": ["In transit", "It is on its way", "arriving soon", "Arrives Monday"],
    "src/atrium/watches.py:<module>:f8dad500": ["Your item has shipped", "Shipment created", "label created", "unshipped"],
    "src/atrium/watches.py:clean_package:088c52e8": [" 1Z 999 AA1\t0123 ", ""],
    "src/atrium/watches.py:clean_package:f0ec7669": ["1Z999AA10123456784", "abc-12", "short", "x" * 41, "with space 123"],
    "src/atrium/web/app.py:RULE_ID_PREFIX": ["mail-rule: Subject text", "no prefix", ": empty", "a b: spaced"],
    "src/atrium/web/markup.py:EMPTY_HEAD": ["<table>\n<thead>\n<tr>\n<th></th>\n<th> </th>\n</tr>\n</thead>\n<tbody>", "<thead><tr><th>x</th></tr></thead>"],
    "src/atrium/web/markup.py:TASK": ["<li>[ ] todo</li><li><p>[x] done</p></li><li>\n<p>[X] Upper", "<li>[y] no"],
    "src/atrium/web/markup.py:HREF": ['<a href="https://a.test">x</a> <a href="">e</a>', "href='single'"],
}
USER_SITES = (
    "src/atrium/media/channels.py:compile_pattern:38c283f4",
    "src/atrium/money.py:match_alias:b4261d17",
    "src/atrium/money.py:add_alias:b686849e",
    "src/atrium/rules.py:condition_matches:afff542b",
    "src/atrium/rules.py:validate_condition:9fc389e6",
)
INPUTS.update({site: USER_INPUTS for site in USER_SITES})

NOT_PATTERN_CALLS = {
    "src/atrium/notion_sync.py:sync:454c2f17",
    "src/atrium/notion_sync.py:sync:e3f6430c",
}

CALLABLE_REPL = {"src/atrium/imap.py:utf7_decode:967eaba6", "src/atrium/notion_markdown.py:convert.part:04bafe8f"}

USE_BINDINGS = {
    "src/atrium/media/parse.py:parse_release:1ee66367": [{"cut": 0}, {"cut": 6}, {"cut": 12}, {"cut": 1000}],
}

INDIRECT_USES = {
    "src/atrium/media/channels.py:preview:960f9bf6": [{"patterns": ["src/atrium/media/channels.py:compile_pattern:38c283f4"]}],
    "src/atrium/media/channels.py:poll:960f9bf6": [{"patterns": ["src/atrium/media/channels.py:compile_pattern:38c283f4"]}],
    "src/atrium/notion_markdown.py:plain:e19c82f8": [
        {"patterns": ["src/atrium/notion_markdown.py:LINK"], "locals": {"replacement": r"\1"}},
        {"patterns": [f"src/atrium/notion_markdown.py:{name}" for name in ("HEADING", "LIST_MARK", "QUOTE", "RULE", "EMPHASIS", "TAG", "CUT_TAG")], "locals": {"replacement": ""}},
        {"patterns": ["src/atrium/notion_markdown.py:ESCAPE"], "locals": {"replacement": r"\1"}},
    ],
    "src/atrium/watches.py:find_tracking.add:b90141b9": [{"patterns": [f"src/atrium/watches.py:{name}" for name in ("UPS_RE", "S10_RE", "USPS_RE", "DHL_JD_RE", "FEDEX_RE", "DHL_RE")]}],
    "src/atrium/watches.py:classify_status:fb738b81": [{"patterns": ["src/atrium/watches.py:<module>:" + digest for digest in ("c92625d2", "39489f00", "4e8ce3b3", "cd61c20e", "fb0585ba", "f8dad500")]}],
}
