package attachment

import (
	"archive/zip"
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

func officeZip(t *testing.T, parts map[string]string) *zip.Reader {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, content := range parts {
		writer, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()))
	if err != nil {
		t.Fatal(err)
	}
	return reader
}

// Item 2ep: a workbook arrives as one table per sheet, rows and columns intact,
// shared strings resolved, numbers as written, merged cells expanded.
func TestWorkbookIngestKeepsSheetsRowsColumnsAndNumbers(t *testing.T) {
	reader := officeZip(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Tasks" sheetId="1" r:id="rId1"/><sheet name="Notes" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/><Relationship Id="rId2" Target="worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml":       `<sst><si><t>Order</t></si><si><t>Step</t></si><si><r><t>Install </t></r><r><t>driver</t></r></si><si><t>Map | queue</t></si></sst>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2"><v>10</v></c><c r="B2" t="s"><v>2</v></c></row><row r="3"><c r="A3"><v>20</v></c><c r="B3" t="s"><v>3</v></c></row></sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>Merged</t></is></c></row></sheetData><mergeCells><mergeCell ref="A1:B1"/></mergeCells></worksheet>`,
	})
	text, ok, err := StructuredOffice(reader, ".xlsx", 1<<20)
	if err != nil || !ok {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
	for _, want := range []string{"## Tasks", "| Order | Step |", "| 10 | Install driver |", "| 20 | Map \\| queue |", "## Notes", "| Merged | Merged |"} {
		if !strings.Contains(text, want) {
			t.Fatalf("workbook markdown lacks %q:\n%s", want, text)
		}
	}
}

func TestDocumentIngestKeepsHeadingsParagraphsListsAndTablesInOrder(t *testing.T) {
	reader := officeZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body>` +
			`<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Runbook</w:t></w:r></w:p>` +
			`<w:p><w:r><w:t>Before you start.</w:t></w:r></w:p>` +
			`<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/></w:numPr></w:pPr><w:r><w:t>Stop the queue</w:t></w:r></w:p>` +
			`<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Order</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Step</w:t></w:r></w:p></w:tc></w:tr>` +
			`<w:tr><w:tc><w:p><w:r><w:t>1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>Map printers</w:t></w:r></w:p></w:tc></w:tr></w:tbl>` +
			`</w:body></w:document>`,
	})
	text, ok, err := StructuredOffice(reader, ".docx", 1<<20)
	if err != nil || !ok {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
	order := []string{"# Runbook", "Before you start.", "- Stop the queue", "| Order | Step |", "| 1 | Map printers |"}
	at := 0
	for _, want := range order {
		index := strings.Index(text[at:], want)
		if index < 0 {
			t.Fatalf("document markdown lacks %q in order:\n%s", want, text)
		}
		at += index + len(want)
	}
}

func TestOfficeIngestStopsAtTheByteBudget(t *testing.T) {
	reader := officeZip(t, map[string]string{
		"word/document.xml": `<w:document xmlns:w="w"><w:body><w:p><w:r><w:t>` + strings.Repeat("x", 4096) + `</w:t></w:r></w:p></w:body></w:document>`,
	})
	if _, _, err := StructuredOffice(reader, ".docx", 1024); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err=%v, want the byte budget", err)
	}
}

// The operator's retained workbook, when one is named. Opt-in: it is the
// operator's file and is never committed.
func TestRetainedUniflowWorkbookIngests(t *testing.T) {
	path := os.Getenv("AGENTB_UNIFLOW_XLSX")
	if path == "" {
		t.Skip("set AGENTB_UNIFLOW_XLSX to the retained workbook")
	}
	text, ok, err := ExtractStructuredOffice(path, 32<<20)
	if err != nil || !ok {
		t.Fatalf("ok=%t err=%v", ok, err)
	}
	if out := os.Getenv("AGENTB_UNIFLOW_OUT"); out != "" {
		if err := os.WriteFile(out, text, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(string(text), "## ") || !strings.Contains(string(text), "| --- |") {
		t.Fatalf("no sheet table:\n%s", text)
	}
}

// v0.65.0/W15 cold review: merge ranges and far-apart cells cannot make the
// grid or the Markdown unbounded.
func TestWorkbookIngestIsBoundedAgainstMergesAndSparseCells(t *testing.T) {
	merges := strings.Repeat(`<mergeCell ref="A1:ALL10000"/>`, 50)
	reader := officeZip(t, map[string]string{
		"xl/workbook.xml":            `<workbook xmlns:r="r"><sheets><sheet name="Bomb" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>x</t></is></c></row><row r="1048576"><c r="XFD1048576" t="inlineStr"><is><t>far</t></is></c></row></sheetData><mergeCells>` + merges + `</mergeCells></worksheet>`,
	})
	done := make(chan struct{})
	var text string
	var err error
	go func() { text, _, err = StructuredOffice(reader, ".xlsx", 32<<20); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("bounded ingest did not finish")
	}
	if err == nil && (!strings.Contains(text, "(truncated:") || len(text) > 32<<20) {
		t.Fatalf("unbounded or unmarked output: %d bytes", len(text))
	}
}
