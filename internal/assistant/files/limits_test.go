package files

import (
	"archive/zip"
	"bytes"
	"strconv"
	"strings"
	"testing"
)

func plainXLSX(t *testing.T, rows string, dimension string, styleIDs string, props string) []byte {
	t.Helper()
	dim := ""
	if dimension != "" {
		dim = `<dimension ref="` + dimension + `"/>`
	}
	sheet := `<worksheet xmlns="` + nsSheet + `">` + dim + `<sheetData>` + rows + `</sheetData></worksheet>`
	entries := [][2]string{
		{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="` + workbookTypes[3] + `"/></Types>`},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="` + relOfficeDoc + `" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", `<workbook xmlns="` + nsSheet + `" xmlns:r="` + nsOfficeRels + `">` + props + `<sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`},
		{"xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="x/worksheet" Target="worksheets/sheet1.xml"/></Relationships>`},
		{"xl/worksheets/sheet1.xml", sheet},
	}
	if styleIDs != "" {
		var xfs strings.Builder
		for id := range strings.SplitSeq(styleIDs, ",") {
			xfs.WriteString(`<xf numFmtId="` + id + `"/>`)
		}
		entries = append(entries, [2]string{"xl/styles.xml", `<styleSheet xmlns="` + nsSheet + `"><cellXfs>` + xfs.String() + `</cellXfs></styleSheet>`})
	}
	return zipOf(t, entries...)
}

func TestOpenpyxlParity(t *testing.T) {
	for _, c := range openpyxlParity {
		t.Run(c.name, func(t *testing.T) {
			dim := ""
			if c.hasDimension {
				dim = c.dimension
			}
			data := plainXLSX(t, c.rows, dim, c.styles, c.props)
			info, err := Inspect("d.xlsx", data, "")
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr {
					t.Fatalf("error %v, Python %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("error %v, Python text %q", err, c.want)
			}
			if *info.Text != c.want {
				t.Errorf("text %q, Python %q", *info.Text, c.want)
			}
		})
	}
}

func TestMimeTypesAreCPythonDefaultsOnly(t *testing.T) {
	for name, want := range map[string]string{
		"memo.rtf":           "text/rtf",
		"invite.ics":         "application/octet-stream",
		"graph.dot":          "application/msword",
		"song.mp3":           "audio/mpeg",
		"a.tar.gz":           "application/x-tar",
		"a.tgz":              "application/x-tar",
		"clip.mp4":           "video/mp4",
		"photo.heic":         "image/heic",
		"style.less":         "application/octet-stream",
		"UPPER.JPEG":         "image/jpeg",
		"sheet.xls":          "application/vnd.ms-excel",
		"note.eml":           "message/rfc822",
		"a.svgz":             "image/svg+xml",
		"a.PICT":             "application/octet-stream",
		"a.js.gz":            "text/javascript",
		"data:,hello":        "text/plain",
		"data:x/y;a,b":       "x/y",
		"http://h/p.pdf?x=1": "application/pdf",
		`C:\x\y.txt`:         "text/plain",
	} {
		if got := MediaTypeOf(name, ""); got != want {
			t.Errorf("MediaTypeOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestBombedMembersAreRefusedNotRead(t *testing.T) {
	big := strings.Repeat(" ", treePartCap+1)
	cases := map[string][]byte{
		"docx part over the tree cap":     zipOf(t, [2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>` + big}),
		"xlsx content types over the cap": zipOf(t, [2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"/>` + big}),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			for _, file := range []string{"a.docx", "a.xlsx"} {
				_, err := Inspect(file, data, "")
				fe, ok := err.(FileError)
				if !ok || !strings.Contains(fe.Error(), "too large") {
					t.Errorf("%s: error %v", file, err)
				}
			}
		})
	}
}

func TestHugeDeflatedSheetStopsAtTheCap(t *testing.T) {
	data := sheetBomb(t, 1, streamPartCap+(1<<20))
	if len(data) > 1<<20 {
		t.Fatalf("the bomb is %d bytes", len(data))
	}
	_, err := Inspect("a.xlsx", data, "")
	if fe, ok := err.(FileError); !ok || !strings.Contains(fe.Error(), "too large") {
		t.Errorf("error %v", err)
	}
}

func TestSheetsTogetherStopAtTheArchiveCap(t *testing.T) {
	data := sheetBomb(t, 5, streamPartCap-(1<<20))
	_, err := Inspect("a.xlsx", data, "")
	if fe, ok := err.(FileError); !ok || !strings.Contains(fe.Error(), "too large") {
		t.Errorf("error %v", err)
	}
}

func sheetBomb(t *testing.T, sheets, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name, body string, method uint16) {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	var book, rels strings.Builder
	for i := 1; i <= sheets; i++ {
		id := strconv.Itoa(i)
		book.WriteString(`<sheet name="S` + id + `" sheetId="` + id + `" r:id="rId` + id + `"/>`)
		rels.WriteString(`<Relationship Id="rId` + id + `" Type="x/worksheet" Target="worksheets/sheet` + id + `.xml"/>`)
	}
	put("[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="`+workbookTypes[3]+`"/></Types>`, zip.Deflate)
	put("_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="`+relOfficeDoc+`" Target="xl/workbook.xml"/></Relationships>`, zip.Deflate)
	put("xl/workbook.xml", `<workbook xmlns="`+nsSheet+`" xmlns:r="`+nsOfficeRels+`"><sheets>`+book.String()+`</sheets></workbook>`, zip.Deflate)
	put("xl/_rels/workbook.xml.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`+rels.String()+`</Relationships>`, zip.Deflate)
	body := `<worksheet xmlns="` + nsSheet + `"><sheetData>` + strings.Repeat(" ", size) + `</sheetData></worksheet>`
	for i := 1; i <= sheets; i++ {
		put("xl/worksheets/sheet"+strconv.Itoa(i)+".xml", body, zip.Deflate)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func docxFrom(t *testing.T, body string) []byte {
	t.Helper()
	return zipOf(t,
		[2]string{"[Content_Types].xml", `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="` + typeWordMain + `"/></Types>`},
		[2]string{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="` + relOfficeDoc + `" Target="word/document.xml"/></Relationships>`},
		[2]string{"word/document.xml", `<w:document xmlns:w="` + nsWord + `"><w:body>` + body + `</w:body></w:document>`})
}

func TestSheetGridAndTextAreBounded(t *testing.T) {
	cases := map[string][]byte{
		"row number past Excel":          plainXLSX(t, `<row r="2000000000"><c r="A1"><v>1</v></c></row>`, "", "", ""),
		"row number just past Excel":     plainXLSX(t, `<row r="`+strconv.Itoa(maxSheetRows+1)+`"/>`, "", "", ""),
		"dimension rows past Excel":      plainXLSX(t, `<row r="`+strconv.Itoa(maxSheetRows+1)+`"/>`, "A1:A"+strconv.Itoa(maxSheetRows+5), "", ""),
		"dimension columns past Excel":   plainXLSX(t, `<row r="1"/>`, "A1:ZZZ2", "", ""),
		"cell column past Excel":         plainXLSX(t, `<row r="1"><c r="XFE1"><v>1</v></c></row>`, "", "", ""),
		"padding rows past the text cap": plainXLSX(t, `<row r="1"/><row r="`+strconv.Itoa(maxSheetRows)+`"/>`, "A1:XFD"+strconv.Itoa(maxSheetRows), "", ""),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Inspect("a.xlsx", data, "")
			if fe, ok := err.(FileError); !ok || !strings.Contains(fe.Error(), "too large") {
				t.Errorf("error %v", err)
			}
		})
	}
	within := plainXLSX(t, `<row r="`+strconv.Itoa(maxSheetRows)+`"><c r="XFD`+strconv.Itoa(maxSheetRows)+`"><v>1</v></c></row>`, "", "", "")
	if _, err := Inspect("a.xlsx", within, ""); err != nil {
		t.Errorf("the last cell of an Excel sheet: %v", err)
	}
}

func TestDocxGridSpanAndTableSizeAreBounded(t *testing.T) {
	spanned := func(span string) []byte {
		return docxFrom(t, `<w:tbl><w:tr><w:tc><w:tcPr><w:gridSpan w:val="`+span+`"/></w:tcPr><w:p><w:r><w:t>x</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`)
	}
	info, err := Inspect("a.docx", spanned("2000000000"), "")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSuffix(strings.Repeat("x\t", maxGridSpan), "\t"); !strings.HasSuffix(*info.Text, want) {
		t.Errorf("a span past Word's %d columns is clamped: %q", maxGridSpan, *info.Text)
	}
	info, err = Inspect("a.docx", spanned("63"), "")
	if err != nil || strings.Count(*info.Text, "x") != 63 {
		t.Errorf("a span of 63 is read as is: %v %v", err, info)
	}
	cells := strings.Repeat(`<w:tc/>`, 3000)
	rows := strings.Repeat(`<w:tr>`+cells+`</w:tr>`, 400)
	if _, err := Inspect("a.docx", docxFrom(t, `<w:tbl>`+rows+`</w:tbl>`), ""); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("a table with %d cells: %v", 3000*400, err)
	}
}

func TestLongVerticalMergeChainIsLinear(t *testing.T) {
	var rows strings.Builder
	rows.WriteString(`<w:tr><w:tc><w:tcPr><w:vMerge w:val="restart"/></w:tcPr><w:p><w:r><w:t>top</w:t></w:r></w:p></w:tc></w:tr>`)
	const chain = 20000
	for range chain {
		rows.WriteString(`<w:tr><w:tc><w:tcPr><w:vMerge/></w:tcPr><w:p/></w:tc></w:tr>`)
	}
	info, err := Inspect("a.docx", docxFrom(t, `<w:tbl>`+rows.String()+`</w:tbl>`), "")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(*info.Text, "top"); got != chain+1 {
		t.Errorf("%d rows repeat the merged cell", got)
	}
}

func TestDocxTextBoundIsTheRequestBudget(t *testing.T) {
	paragraph := `<w:p><w:r><w:t>` + strings.Repeat("a", 1<<20) + `</w:t></w:r></w:p>`
	data := docxFrom(t, strings.Repeat(paragraph, maxTextBytes/(1<<20)+2))
	if _, err := Inspect("a.docx", data, ""); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("error %v", err)
	}
}
