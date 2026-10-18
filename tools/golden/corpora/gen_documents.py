#!/usr/bin/env python3
import datetime
import io
import mimetypes
import re
import sys
import zipfile
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from corpus import b64, emit, error, use_source_tree, versions  # noqa: E402

use_source_tree()

import docx  # noqa: E402
import openpyxl  # noqa: E402

from atrium.agent import files  # noqa: E402

# Python's mimetypes also reads the host's mime.types files; the corpus pins the built-in table only.
mimetypes.init(files=[])

ZIP_TIME = (2026, 1, 1, 0, 0, 0)
CORE_DATE = re.compile(rb"(<dcterms:(?:created|modified)[^>]*>)[^<]*")

W = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
R = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
WORD_FEATURES_XML = f"""<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="{W}" xmlns:r="{R}"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Heading with </w:t></w:r><w:r><w:rPr><w:b/></w:rPr><w:t>bold</w:t></w:r></w:p>
<w:p><w:r><w:t xml:space="preserve">  leading and trailing  </w:t></w:r></w:p>
<w:p><w:r><w:t>col1</w:t></w:r><w:r><w:tab/></w:r><w:r><w:t>col2</w:t></w:r><w:r><w:br/></w:r><w:r><w:t>after break</w:t></w:r><w:r><w:cr/></w:r><w:r><w:t>after cr</w:t></w:r></w:p>
<w:p><w:r><w:t>see </w:t></w:r><w:hyperlink r:id="rIdMissing"><w:r><w:t>the link</w:t></w:r></w:hyperlink><w:r><w:t> here</w:t></w:r></w:p>
<w:p><w:r><w:fldChar w:fldCharType="begin"/></w:r><w:r><w:instrText xml:space="preserve"> PAGE </w:instrText></w:r><w:r><w:fldChar w:fldCharType="separate"/></w:r><w:r><w:t>7</w:t></w:r><w:r><w:fldChar w:fldCharType="end"/></w:r></w:p>
<w:sdt><w:sdtContent><w:p><w:r><w:t>inside a content control</w:t></w:r></w:p></w:sdtContent></w:sdt>
<w:p><w:ins w:id="1" w:author="a"><w:r><w:t>inserted</w:t></w:r></w:ins><w:del w:id="2" w:author="a"><w:r><w:delText>deleted</w:delText></w:r></w:del></w:p>
<w:p><w:r><w:t>soft</w:t></w:r><w:r><w:softHyphen/></w:r><w:r><w:t>hyphen</w:t></w:r><w:r><w:noBreakHyphen/></w:r><w:r><w:t>nb</w:t></w:r></w:p>
<w:p/>
<w:tbl>
<w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:gridSpan w:val="2"/></w:tcPr><w:p><w:r><w:t> spanned </w:t></w:r></w:p></w:tc></w:tr>
<w:tr><w:tc><w:p><w:r><w:t>A2</w:t></w:r></w:p><w:p><w:r><w:t>second para</w:t></w:r></w:p></w:tc><w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>merged down</w:t></w:r></w:p></w:tc><w:tc><w:p/></w:tc></w:tr>
<w:tr><w:tc><w:tbl><w:tr><w:tc><w:p><w:r><w:t>nested</w:t></w:r></w:p></w:tc></w:tr></w:tbl><w:p/></w:tc><w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc><w:tc><w:p><w:r><w:t>C3</w:t></w:r></w:p></w:tc></w:tr>
</w:tbl>
<w:p><w:r><w:t>after table</w:t></w:r></w:p>
<w:sectPr/>
</w:body></w:document>
"""


def repack(data, replace=None):
    replace = replace or {}
    source = zipfile.ZipFile(io.BytesIO(data))
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as target:
        for info in source.infolist():
            content = replace.get(info.filename, source.read(info.filename))
            if info.filename == "docProps/core.xml":
                content = CORE_DATE.sub(rb"\g<1>2026-01-01T00:00:00Z", content)
            entry = zipfile.ZipInfo(info.filename, ZIP_TIME)
            entry.external_attr = 0o644 << 16
            target.writestr(entry, content)
    return out.getvalue()


def save(document):
    buffer = io.BytesIO()
    document.save(buffer)
    return buffer.getvalue()


def plain_docx():
    document = docx.Document()
    document.core_properties.created = datetime.datetime(2026, 1, 1)
    document.core_properties.modified = datetime.datetime(2026, 1, 1)
    document.add_heading("Quarterly notes", level=1)
    document.add_paragraph("First paragraph with ünïcödé, emoji 🎉 and CJK 日本語.")
    document.add_paragraph("")
    document.add_paragraph("Bullet one", style="List Bullet")
    document.add_paragraph("Bullet two", style="List Bullet")
    run = document.add_paragraph().add_run("Line one")
    run.add_break()
    run.add_text("Line two\twith tab")
    table = document.add_table(rows=3, cols=3)
    for r, row in enumerate([["Name", "Amount", "Note"], ["Coffee", "4.50", " spaced "], ["", "", ""]]):
        for c, value in enumerate(row):
            table.cell(r, c).text = value
    second = document.add_table(rows=2, cols=2)
    second.cell(0, 0).merge(second.cell(0, 1)).text = "merged across"
    second.cell(1, 0).text = "x"
    second.cell(1, 1).text = "y\nmultiline"
    document.add_paragraph("Closing paragraph.")
    return repack(save(document))


def empty_docx():
    document = docx.Document()
    return repack(save(document))


def word_features_docx():
    return repack(save(docx.Document()), {"word/document.xml": WORD_FEATURES_XML.encode()})


def workbook():
    book = openpyxl.Workbook()
    book.properties.created = datetime.datetime(2026, 1, 1)
    return book


def save_book(book):
    buffer = io.BytesIO()
    book.save(buffer)
    return repack(buffer.getvalue())


def data_xlsx():
    book = workbook()
    sheet = book.active
    sheet.title = "Data"
    sheet.append(["Date", "Item", "Amount", "Paid", "Note"])
    sheet.append([datetime.datetime(2026, 3, 5, 14, 30), "Coffee, large", 4.5, True, 'He said "hi"'])
    sheet.append([datetime.date(2026, 3, 6), "Rent", 1200, False, "line one\nline two"])
    sheet.append([datetime.time(9, 15), "Fraction", 0.1 + 0.2, None, "  padded  "])
    sheet.append([None, "Big", 1e20, None, "ünïcödé 🎉"])
    sheet.append([None, "Small", 1.5e-7, None, "=not a formula"])
    sheet.append([None, "Negative", -42, None, None])
    sheet.append([None, "Formula", "=C3*2", None, ""])
    sheet.append([])
    sheet.append([None, None, None, None, "trailing only"])
    sheet["G2"] = "far column"
    sheet["C9"].number_format = "0.00%"
    sheet["C9"] = 0.125
    book.create_sheet("Empty")
    other = book.create_sheet("Ünï, sheet")
    other.append(["tab\tinside", "semi;colon", "quote'single"])
    other.append([12345678901234567890, 3.0, "007"])
    return save_book(book)


def single_xlsx():
    book = workbook()
    book.active.title = "Only"
    book.active["A1"] = "one cell"
    return save_book(book)


def zipped(entries):
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", zipfile.ZIP_STORED) as target:
        for name, content in entries:
            target.writestr(zipfile.ZipInfo(name, ZIP_TIME), content)
    return out.getvalue()


def fixtures():
    plain = plain_docx()
    return {
        "plain.docx": plain,
        "empty.docx": empty_docx(),
        "word_features.docx": word_features_docx(),
        "data.xlsx": data_xlsx(),
        "single.xlsx": single_xlsx(),
        "truncated.docx": plain[: len(plain) // 2],
        "not_office.zip": zipped([("readme.txt", b"just a zip")]),
        "garbage.bin": b"PK\x03\x04 this is not a zip at all",
    }


# (case, name, data, supplied media type); data is a fixture path, raw bytes, or (byte, count) to repeat.
INSPECT = [
    ("docx_plain", "Notes.docx", "plain.docx", None),
    ("docx_empty", "empty.docx", "empty.docx", None),
    ("docx_word_features", "features.DOCX", "word_features.docx", None),
    ("docx_by_supplied_type", "upload", "plain.docx", files.DOCX),
    ("xlsx_data", "Budget.xlsx", "data.xlsx", None),
    ("xlsx_single", "single.xlsx", "single.xlsx", "application/octet-stream"),
    ("docx_truncated", "broken.docx", "truncated.docx", None),
    ("docx_not_office", "fake.docx", "not_office.zip", None),
    ("docx_garbage", "garbage.docx", "garbage.bin", None),
    ("xlsx_is_docx", "wrong.xlsx", "plain.docx", None),
    ("xlsx_garbage", "garbage.xlsx", "garbage.bin", None),
    ("xlsx_empty_bytes", "empty.xlsx", b"", None),
    ("docx_empty_bytes", "empty.docx", b"", None),
    ("text_utf8", "notes.txt", "Hello ünïcödé 🎉\nline two\n".encode(), None),
    ("text_bom", "bom.txt", b"\xef\xbb\xbfwith bom", None),
    ("text_invalid_utf8", "latin.txt", b"caf\xe9 \xff\xfe end", None),
    ("text_empty", "empty.txt", b"", None),
    ("text_crlf", "crlf.md", b"# Title\r\n\r\nbody\r\n", None),
    ("text_nul_early", "data.csv", b"a,b\n\x00\n", None),
    ("text_nul_after_8192", "late.log", b"a" * 8192 + b"\x00tail", None),
    ("text_nul_at_8191", "edge.log", b"a" * 8191 + b"\x00", None),
    ("json", "data.json", b'{"a": [1, 2, "\\u00e9"]}', None),
    ("json_supplied_with_params", "blob", b"{}", "Application/JSON; charset=utf-8"),
    ("svg_plus_xml", "drawing", b"<svg/>", "image/svg+xml"),
    ("ld_json", "doc", b"{}", "application/ld+json"),
    ("code_suffix", "main.GO", b"package main\n", None),
    ("nix_suffix", "flake.nix", b"{ }\n", None),
    ("html", "page.html", b"<p>hi</p>", "application/octet-stream"),
    ("text_supplied_type", "README", b"plain readme", "text/plain"),
    ("unknown_binary", "archive.zip", b"PK", None),
    ("unknown_no_suffix", "blob", b"data", None),
    ("unknown_supplied", "blob", b"data", "application/x-thing"),
    ("guess_type_builtin", "song.mp3", b"ID3", None),
    ("guess_type_builtin_text", "style.less", b"a{}", None),
    ("pdf_one_page", "doc.pdf", b"%PDF-1.4\n<< /Type /Page /Parent 2 0 R >>\n<< /Type/Pages /Kids [] >>\n", None),
    ("pdf_three_pages", "doc.PDF", b"/Type /Page\n/Type/Page\n/Type  /Page>>\n/Type /Pages\n/Type /PageLabel\n/Type /Page/\n", None),
    ("pdf_no_pages", "scan.pdf", b"%PDF-1.7 nothing", None),
    ("pdf_supplied", "upload", b"/Type /Page", "application/pdf"),
    ("image_png", "photo.PNG", b"\x89PNG\r\n\x1a\n", None),
    ("image_webp_supplied", "upload", b"RIFF", "image/webp"),
    ("image_heic", "photo.heic", b"....", None),
    ("image_svg_suffix", "logo.svg", b"<svg/>", None),
    ("image_over_limit", "big.jpg", (b"\xff", (5 << 20) + 1), None),
    ("image_at_limit", "big.jpg", (b"\xff", 5 << 20), None),
    ("file_over_limit", "big.txt", (b"a", (32 << 20) + 1), None),
]

SAFE_NAMES = ["report.pdf", "../../etc/passwd", "My Résumé (final).docx", "...hidden", "___x", "", None, "a/b/c.txt", "con:stream", "名前.txt", "x" * 100 + ".txt", ".", "-dash.txt", "spaces   and\ttabs.md"]
MEDIA_TYPES = [
    ("a.MD", None), ("a.csv", "application/vnd.ms-excel"), ("a", "Text/CSV; charset=utf-8"), ("a", "application/octet-stream"), ("a", " "),
    ("archive.tar.gz", None), ("a.yml", None), ("a.unknownext", None), ("", None), (None, "image/jpeg"), ("a.jpeg", "text/plain"), ("Makefile", None),
]
KINDS = [files.PDF_TYPE, "image/jpeg", "image/bmp", files.DOCX, files.XLSX, "text/anything", "application/json", "application/rss+xml", "application/vnd.api+json", "application/zip", "application/octet-stream", "TEXT/PLAIN", ""]
BUDGETS = [([("a.pdf", 10000)], 200000), ([("a.pdf", 150000), ("b.txt", 60000)], 200000), ([("a", 1000), ("b", 2000)], 4000), ([("a", 3000), ("b", 3000)], 4000), ([("big", 999999)], 10**9)]


def payload(data, fixture_bytes):
    if isinstance(data, str):
        return {"file": f"documents/{data}"}, fixture_bytes[data]
    if isinstance(data, tuple):
        byte, count = data
        return {"repeat_b64": b64(byte), "count": count}, byte * count
    return {"b64": b64(data)}, data


def build(fixture_bytes):
    records = []
    for case, name, data, supplied in INSPECT:
        described, raw = payload(data, fixture_bytes)
        record = {"fn": "inspect", "case": case, "name": name, "supplied_type": supplied, "data": described}
        try:
            record["output"] = files.inspect(name, raw, supplied)
        except files.FileError as e:
            record["error"] = error(e)
        records.append(record)
    for name in SAFE_NAMES:
        records.append({"fn": "safe_name", "input": [name], "output": files.safe_name(name)})
    for name, supplied in MEDIA_TYPES:
        records.append({"fn": "media_type_of", "input": [name, supplied], "output": files.media_type_of(name, supplied)})
    for media_type in KINDS:
        records.append({"fn": "kind_of", "input": [media_type], "output": files.kind_of(media_type)})
    for names_and_tokens, limit in BUDGETS:
        try:
            files.check_budget(names_and_tokens, limit)
            result = {"output": None}
        except files.FileError as e:
            result = {"error": error(e)}
        records.append({"fn": "check_budget", "input": [names_and_tokens, limit], **result})
    return records


if __name__ == "__main__":
    made = fixtures()
    emit(
        "documents",
        build(made),
        {
            "source": "src/atrium/agent/files.py",
            "functions": ["inspect", "safe_name", "media_type_of", "kind_of", "check_budget"],
            "versions": versions("python-docx", "openpyxl"),
            "mimetypes": "built-in table only (mimetypes.init(files=[]))",
        },
        made,
    )
