package attachment

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Item 2ep: Office attachments keep their structure. A workbook becomes one
// Markdown table per sheet (sheet name as its heading, rows and columns intact,
// formulas as their cached values, merged cells expanded); a document becomes
// headings, paragraphs, list items and tables in reading order. Standard
// library only: archive/zip and encoding/xml, which never resolve external
// entities.

// tableHeaderRepeat is how many body rows a sheet table carries before its
// header row is repeated, so a byte window read from the middle of a large
// sheet still names its columns.
const tableHeaderRepeat = 100

type zipBudget struct {
	remaining int64
	limit     int64
}

func (b *zipBudget) open(files map[string]*zip.File, name string) (io.ReadCloser, error) {
	file, ok := files[strings.ToLower(name)]
	if !ok {
		return nil, fmt.Errorf("office part %s is missing", name)
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	return &budgetReader{ReadCloser: reader, budget: b}, nil
}

type budgetReader struct {
	io.ReadCloser
	budget *zipBudget
}

func (r *budgetReader) Read(p []byte) (int, error) {
	if r.budget.remaining <= 0 {
		return 0, fmt.Errorf("office XML exceeds %d bytes", r.budget.limit)
	}
	if int64(len(p)) > r.budget.remaining {
		p = p[:r.budget.remaining]
	}
	n, err := r.ReadCloser.Read(p)
	r.budget.remaining -= int64(n)
	return n, err
}

func zipIndex(reader *zip.Reader) map[string]*zip.File {
	files := make(map[string]*zip.File, len(reader.File))
	for _, file := range reader.File {
		files[strings.ToLower(path.Clean(strings.ReplaceAll(file.Name, "\\", "/")))] = file
	}
	return files
}

// StructuredOffice returns the Markdown form of an .xlsx or .docx, reading at
// most maxBytes of uncompressed XML. ok is false for any other extension.
func StructuredOffice(reader *zip.Reader, extension string, maxBytes int64) (text string, ok bool, err error) {
	budget := &zipBudget{remaining: maxBytes, limit: maxBytes}
	files := zipIndex(reader)
	switch strings.ToLower(extension) {
	case ".xlsx":
		text, err = workbookMarkdown(files, budget)
		return text, true, err
	case ".docx":
		text, err = documentMarkdown(files, budget)
		return text, true, err
	}
	return "", false, nil
}

// --- workbook ---

func workbookMarkdown(files map[string]*zip.File, budget *zipBudget) (string, error) {
	shared, err := sharedStrings(files, budget)
	if err != nil {
		return "", err
	}
	sheets, err := workbookSheets(files, budget)
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for _, sheet := range sheets {
		grid, err := sheetGrid(files, budget, sheet.part, shared)
		if err != nil {
			return "", err
		}
		if out.Len() > 0 {
			out.WriteString("\n")
		}
		out.WriteString("## " + markdownInline(sheet.name) + "\n\n")
		writeGridTable(&out, grid)
		if int64(out.Len()) > budget.limit {
			return "", fmt.Errorf("workbook Markdown exceeds %d bytes", budget.limit)
		}
	}
	if out.Len() == 0 {
		return "", fmt.Errorf("workbook contains no sheets")
	}
	return out.String(), nil
}

type sheetRef struct{ name, part string }

func workbookSheets(files map[string]*zip.File, budget *zipBudget) ([]sheetRef, error) {
	targets := map[string]string{}
	if rels, err := budget.open(files, "xl/_rels/workbook.xml.rels"); err == nil {
		decoder := xml.NewDecoder(rels)
		for {
			token, tokenErr := decoder.Token()
			if tokenErr == io.EOF {
				break
			}
			if tokenErr != nil {
				rels.Close()
				return nil, tokenErr
			}
			if start, ok := token.(xml.StartElement); ok && start.Name.Local == "Relationship" {
				targets[attr(start, "Id")] = attr(start, "Target")
			}
		}
		rels.Close()
	}
	book, err := budget.open(files, "xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	defer book.Close()
	decoder := xml.NewDecoder(book)
	sheets := []sheetRef{}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		target := targets[attrSpace(start, "id")]
		if target == "" {
			target = fmt.Sprintf("worksheets/sheet%d.xml", len(sheets)+1)
		}
		part := path.Clean(path.Join("xl", strings.TrimPrefix(target, "/xl/")))
		if strings.HasPrefix(target, "/") {
			part = path.Clean(strings.TrimPrefix(target, "/"))
		}
		sheets = append(sheets, sheetRef{name: attr(start, "name"), part: part})
	}
	return sheets, nil
}

func sharedStrings(files map[string]*zip.File, budget *zipBudget) ([]string, error) {
	reader, err := budget.open(files, "xl/sharedStrings.xml")
	if err != nil {
		return nil, nil
	}
	defer reader.Close()
	decoder := xml.NewDecoder(reader)
	values := []string{}
	var current strings.Builder
	inItem, inText, inPhonetic := false, false, false
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		switch value := token.(type) {
		case xml.StartElement:
			switch value.Name.Local {
			case "si":
				inItem = true
				current.Reset()
			case "rPh":
				inPhonetic = true
			case "t":
				inText = inItem && !inPhonetic
			}
		case xml.EndElement:
			switch value.Name.Local {
			case "si":
				values = append(values, current.String())
				inItem = false
			case "rPh":
				inPhonetic = false
			case "t":
				inText = false
			}
		case xml.CharData:
			if inText {
				current.Write(value)
			}
		}
	}
	return values, nil
}

type cellGrid struct {
	cells     map[[2]int]string
	maxRow    int
	maxCol    int
	minRow    int
	merges    [][4]int
	present   bool
	truncated bool
}

func sheetGrid(files map[string]*zip.File, budget *zipBudget, part string, shared []string) (*cellGrid, error) {
	reader, err := budget.open(files, part)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	grid := &cellGrid{cells: map[[2]int]string{}, minRow: -1}
	decoder := xml.NewDecoder(reader)
	var cellRef, cellType string
	var value, inline strings.Builder
	inValue, inInline, inCell := false, false, false
	rowIndex, colIndex := 0, 0
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return nil, tokenErr
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "row":
				if r, convErr := strconv.Atoi(attr(element, "r")); convErr == nil {
					rowIndex = r - 1
				} else {
					rowIndex++
				}
				colIndex = -1
			case "c":
				inCell = true
				cellRef, cellType = attr(element, "r"), attr(element, "t")
				value.Reset()
				inline.Reset()
				colIndex++
			case "v":
				inValue = inCell
			case "t":
				inInline = inCell && cellType == "inlineStr"
			case "mergeCell":
				if bounds, ok := parseRange(attr(element, "ref")); ok {
					grid.merges = append(grid.merges, bounds)
				}
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "v":
				inValue = false
			case "t":
				inInline = false
			case "c":
				inCell = false
				row, col := rowIndex, colIndex
				if r, c, ok := parseCell(cellRef); ok {
					row, col = r, c
					colIndex = c
				}
				text := value.String()
				switch cellType {
				case "s":
					if index, convErr := strconv.Atoi(strings.TrimSpace(text)); convErr == nil && index >= 0 && index < len(shared) {
						text = shared[index]
					}
				case "inlineStr":
					text = inline.String()
				case "b":
					if strings.TrimSpace(text) == "1" {
						text = "TRUE"
					} else {
						text = "FALSE"
					}
				}
				if text != "" {
					grid.set(row, col, text)
				}
			}
		case xml.CharData:
			if inValue {
				value.Write(element)
			} else if inInline {
				inline.Write(element)
			}
		}
	}
	for _, merge := range grid.merges {
		top := grid.cells[[2]int{merge[0], merge[1]}]
		if top == "" {
			continue
		}
		for r := merge[0]; r <= merge[2] && r-merge[0] < 10000 && !grid.full(); r++ {
			for c := merge[1]; c <= merge[3] && c <= maxSheetColumns && !grid.full(); c++ {
				grid.set(r, c, top)
			}
		}
	}
	return grid, nil
}

// v0.65.0/W15 cold review: a few kilobytes of merge ranges or two far-apart
// cells made the grid (and the Markdown) hundreds of megabytes. A sheet keeps at
// most maxSheetCells cells and maxSheetColumns+1 columns; the rest is marked
// truncated, never silently dropped.
const (
	maxSheetCells   = 200000
	maxSheetColumns = 255
)

func (g *cellGrid) full() bool { return len(g.cells) >= maxSheetCells }

func (g *cellGrid) set(row, col int, text string) {
	if row < 0 || col < 0 || row > 1048575 {
		return
	}
	if col > maxSheetColumns {
		g.truncated = true
		return
	}
	key := [2]int{row, col}
	if _, exists := g.cells[key]; !exists && g.full() {
		g.truncated = true
		return
	}
	g.cells[key] = text
	g.present = true
	if row > g.maxRow {
		g.maxRow = row
	}
	if col > g.maxCol {
		g.maxCol = col
	}
	if g.minRow < 0 || row < g.minRow {
		g.minRow = row
	}
}

func writeGridTable(out *strings.Builder, grid *cellGrid) {
	if !grid.present {
		out.WriteString("(empty sheet)\n")
		return
	}
	columns := grid.maxCol + 1
	row := func(r int) []string {
		values := make([]string, columns)
		for c := 0; c < columns; c++ {
			values[c] = markdownCell(grid.cells[[2]int{r, c}])
		}
		return values
	}
	header := row(grid.minRow)
	separator := make([]string, columns)
	for index := range separator {
		separator[index] = "---"
	}
	writeRow := func(values []string) { out.WriteString("| " + strings.Join(values, " | ") + " |\n") }
	writeRow(header)
	writeRow(separator)
	// Only rows that hold a cell are visited; the span between them is empty.
	rows := map[int]bool{}
	for key := range grid.cells {
		if key[0] > grid.minRow {
			rows[key[0]] = true
		}
	}
	order := make([]int, 0, len(rows))
	for r := range rows {
		order = append(order, r)
	}
	sort.Ints(order)
	body := 0
	for _, r := range order {
		values := row(r)
		if strings.Join(values, "") == "" {
			continue
		}
		if body > 0 && body%tableHeaderRepeat == 0 {
			out.WriteString("\n")
			writeRow(header)
			writeRow(separator)
		}
		writeRow(values)
		body++
	}
	if grid.truncated {
		out.WriteString(fmt.Sprintf("\n(truncated: this sheet has more than %d cells or %d columns)\n", maxSheetCells, maxSheetColumns+1))
	}
}

func parseCell(ref string) (row, col int, ok bool) {
	ref = strings.ToUpper(strings.TrimSpace(ref))
	index := 0
	for index < len(ref) && ref[index] >= 'A' && ref[index] <= 'Z' {
		col = col*26 + int(ref[index]-'A'+1)
		index++
		if col > 16384 {
			return 0, 0, false
		}
	}
	if index == 0 || index == len(ref) {
		return 0, 0, false
	}
	number, err := strconv.Atoi(ref[index:])
	if err != nil || number < 1 {
		return 0, 0, false
	}
	return number - 1, col - 1, true
}

func parseRange(ref string) ([4]int, bool) {
	parts := strings.SplitN(ref, ":", 2)
	if len(parts) != 2 {
		return [4]int{}, false
	}
	r1, c1, ok1 := parseCell(parts[0])
	r2, c2, ok2 := parseCell(parts[1])
	if !ok1 || !ok2 || r2 < r1 || c2 < c1 {
		return [4]int{}, false
	}
	return [4]int{r1, c1, r2, c2}, true
}

// --- document ---

func documentMarkdown(files map[string]*zip.File, budget *zipBudget) (string, error) {
	reader, err := budget.open(files, "word/document.xml")
	if err != nil {
		return "", err
	}
	defer reader.Close()
	decoder := xml.NewDecoder(reader)
	var out strings.Builder
	var paragraph strings.Builder
	style, listItem := "", false
	tableDepth := 0
	var table [][]string
	var rowCells []string
	var cellText strings.Builder
	inText := false
	flushParagraph := func() {
		text := strings.TrimSpace(paragraph.String())
		paragraph.Reset()
		if tableDepth > 0 {
			if text != "" {
				if cellText.Len() > 0 {
					cellText.WriteString(" ")
				}
				cellText.WriteString(text)
			}
			return
		}
		if text == "" {
			return
		}
		switch {
		case headingLevel(style) > 0:
			out.WriteString(strings.Repeat("#", headingLevel(style)) + " " + text + "\n\n")
		case listItem:
			out.WriteString("- " + text + "\n")
		default:
			out.WriteString(text + "\n\n")
		}
	}
	for {
		token, tokenErr := decoder.Token()
		if tokenErr == io.EOF {
			break
		}
		if tokenErr != nil {
			return "", tokenErr
		}
		switch element := token.(type) {
		case xml.StartElement:
			switch element.Name.Local {
			case "p":
				style, listItem = "", false
				paragraph.Reset()
			case "pStyle":
				style = attrSpace(element, "val")
			case "numPr":
				listItem = true
			case "t":
				inText = true
			case "tab":
				paragraph.WriteString("\t")
			case "br":
				paragraph.WriteString(" ")
			case "tbl":
				tableDepth++
				if tableDepth == 1 {
					table = nil
				}
			case "tr":
				if tableDepth == 1 {
					rowCells = nil
				}
			case "tc":
				if tableDepth == 1 {
					cellText.Reset()
				}
			}
		case xml.EndElement:
			switch element.Name.Local {
			case "t":
				inText = false
			case "p":
				flushParagraph()
			case "tc":
				if tableDepth == 1 {
					rowCells = append(rowCells, markdownCell(cellText.String()))
				}
			case "tr":
				if tableDepth == 1 {
					table = append(table, rowCells)
				}
			case "tbl":
				if tableDepth == 1 {
					writeRowsTable(&out, table)
				}
				tableDepth--
			}
		case xml.CharData:
			if inText {
				paragraph.Write(element)
			}
		}
	}
	text := strings.TrimSpace(out.String())
	if text == "" {
		return "", fmt.Errorf("document contains no readable text")
	}
	return text + "\n", nil
}

func headingLevel(style string) int {
	lower := strings.ToLower(style)
	if lower == "title" {
		return 1
	}
	if strings.HasPrefix(lower, "heading") {
		if level, err := strconv.Atoi(strings.TrimPrefix(lower, "heading")); err == nil && level >= 1 && level <= 6 {
			return level
		}
	}
	return 0
}

func writeRowsTable(out *strings.Builder, rows [][]string) {
	columns := 0
	for _, row := range rows {
		columns = max(columns, len(row))
	}
	if columns == 0 {
		return
	}
	writeRow := func(values []string) {
		padded := append(append([]string(nil), values...), make([]string, columns-len(values))...)
		out.WriteString("| " + strings.Join(padded, " | ") + " |\n")
	}
	writeRow(rows[0])
	separator := make([]string, columns)
	for index := range separator {
		separator[index] = "---"
	}
	writeRow(separator)
	for _, row := range rows[1:] {
		writeRow(row)
	}
	out.WriteString("\n")
}

// --- shared ---

func attr(element xml.StartElement, local string) string {
	for _, a := range element.Attr {
		if a.Name.Local == local && a.Name.Space == "" {
			return a.Value
		}
	}
	return ""
}

// attrSpace matches a namespaced attribute by its local name (r:id, w:val).
func attrSpace(element xml.StartElement, local string) string {
	for _, a := range element.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func markdownCell(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	return strings.ReplaceAll(value, "|", "\\|")
}

func markdownInline(value string) string { return strings.Join(strings.Fields(value), " ") }

// ExtractStructuredOffice opens the file at path and returns its structured
// Markdown when it is an .xlsx or .docx; ok is false for other Office types,
// which keep the plain text extraction.
func ExtractStructuredOffice(filePath string, maxBytes int64) (text []byte, ok bool, err error) {
	extension := strings.ToLower(path.Ext(strings.ReplaceAll(filePath, "\\", "/")))
	if extension != ".xlsx" && extension != ".docx" {
		return nil, false, nil
	}
	reader, err := zip.OpenReader(filePath)
	if err != nil {
		return nil, true, fmt.Errorf("open office document: %w", err)
	}
	defer reader.Close()
	markdown, _, err := StructuredOffice(&reader.Reader, extension, maxBytes)
	if err != nil {
		return nil, true, err
	}
	return []byte(markdown), true, nil
}
