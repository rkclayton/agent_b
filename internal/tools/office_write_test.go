package tools

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// Item 2ep: Markdown with two ## sections and tables becomes a two-sheet
// workbook; numbers are numbers, and text a model wrote that starts with `=`
// or carries XML stays inert text.
func TestMarkdownBecomesAWorkbookWithInertCells(t *testing.T) {
	markdown := "# Report\n\n## Tasks\n\n| Order | Step |\n| --- | --- |\n| 1 | Map <printers> & queues |\n| 2 | =HYPERLINK(\"http://example.invalid\") |\n\n## Notes | extra\n\n| Note |\n|:--|\n| pipe \\| kept |\n"
	sheets, err := markdownWorkbook(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].name != "Tasks" || len(sheets[0].rows) != 3 || sheets[1].rows[1][0] != "pipe | kept" {
		t.Fatalf("sheets=%+v", sheets)
	}
	workbook, err := buildWorkbook(sheets)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(workbook), int64(len(workbook)))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	for _, file := range reader.File {
		opened, _ := file.Open()
		data, _ := io.ReadAll(opened)
		opened.Close()
		parts[file.Name] = string(data)
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "xl/workbook.xml", "xl/_rels/workbook.xml.rels", "xl/styles.xml", "xl/worksheets/sheet1.xml", "xl/worksheets/sheet2.xml"} {
		if parts[name] == "" {
			t.Fatalf("workbook lacks %s", name)
		}
	}
	sheet := parts["xl/worksheets/sheet1.xml"]
	for _, want := range []string{`<c r="A2"><v>1</v></c>`, `Map &lt;printers&gt; &amp; queues`, `t="inlineStr"><is><t xml:space="preserve">=HYPERLINK(&quot;http://example.invalid&quot;)</t>`} {
		if !strings.Contains(sheet, want) {
			t.Fatalf("sheet1 lacks %q:\n%s", want, sheet)
		}
	}
	if strings.Contains(sheet, "<f>") {
		t.Fatal("a formula element was written")
	}
	if !strings.Contains(parts["xl/workbook.xml"], `name="Notes  extra"`) && !strings.Contains(parts["xl/workbook.xml"], `name="Notes | extra"`) {
		t.Fatalf("sheet names: %s", parts["xl/workbook.xml"])
	}
}

func TestMarkdownWithoutATableIsRefusedWithTheReason(t *testing.T) {
	if _, err := markdownWorkbook("## Empty\n\nno table here\n"); err == nil || !strings.Contains(err.Error(), "no Markdown table") {
		t.Fatalf("err=%v", err)
	}
	if _, err := markdownWorkbook("just prose"); err == nil {
		t.Fatal("prose with no table must be refused")
	}
}

func TestSheetNamesAreValidAndUnique(t *testing.T) {
	used := map[string]bool{}
	first := uniqueSheetName("Q1: [draft]/*?", 1, used)
	second := uniqueSheetName("Q1 draft", 2, used)
	long := uniqueSheetName(strings.Repeat("x", 40), 3, used)
	if strings.ContainsAny(first, `[]:*?/\`) || len([]rune(long)) > 31 || second == "" {
		t.Fatalf("first=%q second=%q long=%q", first, second, long)
	}
}
