package tools

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
)

// Item 2ep: write_file turns Markdown into a real workbook when the path ends
// .xlsx. Each `## heading` names a sheet and the first Markdown table under it
// becomes that sheet; Markdown with no heading makes one sheet. Standard
// library only. Every cell is written as an inline string or a number, never a
// formula, so text a model wrote that starts with `=` stays text in Excel.

var numericCell = regexp.MustCompile(`^-?(?:\d+\.?\d*|\.\d+)(?:[eE][-+]?\d+)?$`)

type workbookSheet struct {
	name string
	rows [][]string
}

// markdownWorkbook parses Markdown into sheets. A section with no table is an
// error naming it, so a model learns why nothing was written.
func markdownWorkbook(markdown string) ([]workbookSheet, error) {
	sheets := []workbookSheet{}
	current := workbookSheet{}
	inTable := false
	flush := func() {
		if current.name != "" || len(current.rows) > 0 {
			sheets = append(sheets, current)
		}
		current = workbookSheet{}
		inTable = false
	}
	for _, raw := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "## ") {
			flush()
			current.name = strings.TrimSpace(strings.TrimPrefix(line, "## "))
			continue
		}
		if !strings.HasPrefix(line, "|") {
			if inTable {
				inTable = false
			}
			continue
		}
		if len(current.rows) > 0 && !inTable {
			continue // only the first table of a section becomes the sheet
		}
		cells := splitMarkdownRow(line)
		if isSeparatorRow(cells) {
			inTable = true
			continue
		}
		inTable = true
		current.rows = append(current.rows, cells)
	}
	flush()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("xlsx content needs at least one Markdown table (one per ## sheet heading)")
	}
	used := map[string]bool{}
	for index := range sheets {
		if len(sheets[index].rows) == 0 {
			return nil, fmt.Errorf("sheet %q has no Markdown table", sheets[index].name)
		}
		sheets[index].name = uniqueSheetName(sheets[index].name, index+1, used)
	}
	return sheets, nil
}

func splitMarkdownRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	if strings.HasSuffix(line, "|") && !strings.HasSuffix(line, "\\|") {
		line = strings.TrimSuffix(line, "|")
	}
	cells := []string{}
	var cell strings.Builder
	for index := 0; index < len(line); index++ {
		if line[index] == '\\' && index+1 < len(line) && line[index+1] == '|' {
			cell.WriteByte('|')
			index++
			continue
		}
		if line[index] == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteByte(line[index])
	}
	return append(cells, strings.TrimSpace(cell.String()))
}

func isSeparatorRow(cells []string) bool {
	for _, cell := range cells {
		trimmed := strings.Trim(cell, " :")
		if trimmed == "" || strings.Trim(trimmed, "-") != "" {
			return false
		}
	}
	return len(cells) > 0
}

func uniqueSheetName(name string, index int, used map[string]bool) string {
	name = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) || r < 0x20 {
			return -1
		}
		return r
	}, strings.TrimSpace(name))
	name = strings.Trim(name, "'")
	if name == "" {
		name = fmt.Sprintf("Sheet%d", index)
	}
	if runes := []rune(name); len(runes) > 31 {
		name = string(runes[:31])
	}
	base, n := name, 2
	for used[strings.ToLower(name)] {
		suffix := fmt.Sprintf(" (%d)", n)
		runes := []rune(base)
		if len(runes)+len([]rune(suffix)) > 31 {
			runes = runes[:31-len([]rune(suffix))]
		}
		name = string(runes) + suffix
		n++
	}
	used[strings.ToLower(name)] = true
	return name
}

// buildWorkbook writes the sheets as an .xlsx package.
func buildWorkbook(sheets []workbookSheet) ([]byte, error) {
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	add := func(name, content string) error {
		writer, err := archive.Create(name)
		if err != nil {
			return err
		}
		_, err = writer.Write([]byte(xml.Header + content))
		return err
	}
	var types, books, rels strings.Builder
	types.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/><Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	books.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets>`)
	rels.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for index, sheet := range sheets {
		number := index + 1
		fmt.Fprintf(&types, `<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, number)
		fmt.Fprintf(&books, `<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, escapeXML(sheet.name), number, number)
		fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, number, number)
		if err := add(fmt.Sprintf("xl/worksheets/sheet%d.xml", number), sheetXML(sheet.rows)); err != nil {
			return nil, err
		}
	}
	fmt.Fprintf(&rels, `<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`, len(sheets)+1)
	types.WriteString(`</Types>`)
	books.WriteString(`</sheets></workbook>`)
	rels.WriteString(`</Relationships>`)
	parts := []struct{ name, content string }{
		{"[Content_Types].xml", types.String()},
		{"_rels/.rels", `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", books.String()},
		{"xl/_rels/workbook.xml.rels", rels.String()},
		{"xl/styles.xml", `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="1"><font><sz val="11"/><name val="Calibri"/></font></fonts><fills count="1"><fill><patternFill patternType="none"/></fill></fills><borders count="1"><border/></borders><cellStyleXfs count="1"><xf/></cellStyleXfs><cellXfs count="1"><xf/></cellXfs></styleSheet>`},
	}
	for _, part := range parts {
		if err := add(part.name, part.content); err != nil {
			return nil, err
		}
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func sheetXML(rows [][]string) string {
	var out strings.Builder
	out.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetData>`)
	for r, row := range rows {
		fmt.Fprintf(&out, `<row r="%d">`, r+1)
		for c, cell := range row {
			if cell == "" {
				continue
			}
			ref := columnName(c) + fmt.Sprint(r+1)
			if numericCell.MatchString(cell) {
				fmt.Fprintf(&out, `<c r="%s"><v>%s</v></c>`, ref, cell)
			} else {
				fmt.Fprintf(&out, `<c r="%s" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`, ref, escapeXML(cell))
			}
		}
		out.WriteString(`</row>`)
	}
	out.WriteString(`</sheetData></worksheet>`)
	return out.String()
}

func columnName(index int) string {
	name := ""
	for index++; index > 0; index = (index - 1) / 26 {
		name = string(rune('A'+(index-1)%26)) + name
	}
	return name
}

func escapeXML(value string) string {
	var out bytes.Buffer
	for _, r := range value {
		// XML 1.0 forbids most control characters; they are dropped, not escaped.
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			continue
		}
		switch r {
		case '&':
			out.WriteString("&amp;")
		case '<':
			out.WriteString("&lt;")
		case '>':
			out.WriteString("&gt;")
		case '"':
			out.WriteString("&quot;")
		case '\'':
			out.WriteString("&apos;")
		default:
			out.WriteRune(r)
		}
	}
	return out.String()
}
