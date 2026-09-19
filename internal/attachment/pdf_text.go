package attachment

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// ErrNoTextLayer is a PDF whose pages carry no text: a scan, for the OCR path.
var ErrNoTextLayer = errors.New("PDF has no text layer")

// ExtractPDFText reads a PDF's text layer locally, page by page (item 2fj),
// with github.com/ledongthuc/pdf (BSD-3-Clause, pure Go, no dependencies).
// The library panics on streams it cannot decode; that is returned as an
// error, never allowed to reach the server. Output stops at maxBytes with a
// line saying so.
func ExtractPDFText(path string, maxBytes int64) (text []byte, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			text, err = nil, fmt.Errorf("read PDF: %v", recovered)
		}
	}()
	file, reader, err := pdf.Open(path)
	if err != nil {
		// A file with a missing or broken cross-reference table is what a
		// hand-written or simply generated PDF often is; readers repair it by
		// finding the objects. So does this, in memory.
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("open PDF: %w", err)
		}
		repaired, ok := repairXref(raw)
		if !ok {
			return nil, fmt.Errorf("open PDF: %w", err)
		}
		reader, err = pdf.NewReader(bytes.NewReader(repaired), int64(len(repaired)))
		if err != nil {
			return nil, fmt.Errorf("open PDF: %w", err)
		}
	} else {
		defer file.Close()
	}
	var output strings.Builder
	found := false
	pages := reader.NumPage()
	for index := 1; index <= pages; index++ {
		page := reader.Page(index)
		if page.V.IsNull() {
			continue
		}
		body, pageErr := page.GetPlainText(nil)
		if pageErr != nil {
			return nil, fmt.Errorf("read PDF page %d: %w", index, pageErr)
		}
		body = strings.TrimSpace(body)
		if body == "" {
			continue
		}
		found = true
		section := fmt.Sprintf("## Page %d\n\n%s\n\n", index, body)
		if maxBytes > 0 && int64(output.Len()+len(section)) > maxBytes {
			fmt.Fprintf(&output, "[text truncated at %d bytes; page %d of %d not included]\n", maxBytes, index, pages)
			break
		}
		output.WriteString(section)
	}
	if !found {
		return nil, ErrNoTextLayer
	}
	return []byte(output.String()), nil
}

var pdfObjectHeader = regexp.MustCompile(`(?m)(?:^|[\r\n])(\d+)[ \t]+(\d+)[ \t]+obj\b`)
var pdfTrailerDict = regexp.MustCompile(`(?s)trailer\s*(<<.*?>>)\s*(?:startxref|%%EOF|$)`)

// repairXref rebuilds a PDF's cross-reference table from the object headers it
// finds and appends it, with the file's own trailer, so a reader that needs a
// valid table can open it. It reports false when there is nothing to rebuild.
func repairXref(raw []byte) ([]byte, bool) {
	raw = fixStreamLengths(raw)
	offsets := map[int]int{}
	maxObject := 0
	for _, match := range pdfObjectHeader.FindAllSubmatchIndex(raw, -1) {
		number, err := strconv.Atoi(string(raw[match[2]:match[3]]))
		if err != nil {
			continue
		}
		offsets[number] = match[2]
		maxObject = max(maxObject, number)
	}
	trailer := pdfTrailerDict.FindAllSubmatch(raw, -1)
	if len(offsets) == 0 || len(trailer) == 0 {
		return nil, false
	}
	dict := string(trailer[len(trailer)-1][1])
	if !strings.Contains(dict, "/Root") {
		return nil, false
	}
	if !strings.Contains(dict, "/Size") {
		dict = strings.TrimSuffix(dict, ">>") + fmt.Sprintf("/Size %d>>", maxObject+1)
	}
	var out bytes.Buffer
	out.Write(raw)
	if !bytes.HasSuffix(raw, []byte("\n")) {
		out.WriteByte('\n')
	}
	start := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", maxObject+1)
	for number := 1; number <= maxObject; number++ {
		if offset, ok := offsets[number]; ok {
			fmt.Fprintf(&out, "%010d 00000 n \n", offset)
		} else {
			out.WriteString("0000000000 65535 f \n")
		}
	}
	fmt.Fprintf(&out, "trailer\n%s\nstartxref\n%d\n%%%%EOF\n", dict, start)
	return out.Bytes(), true
}

var pdfStreamLength = regexp.MustCompile(`/Length\s+(\d+)(\s*[/>])`)

// fixStreamLengths corrects each stream's direct /Length to the bytes between
// "stream" and "endstream", as readers do when a declared length is wrong.
// search walks the file; cursor is how far the output has been copied.
func fixStreamLengths(raw []byte) []byte {
	var out bytes.Buffer
	cursor, search := 0, 0
	for {
		at := bytes.Index(raw[search:], []byte("stream"))
		if at < 0 {
			break
		}
		at += search
		dataStart := at + len("stream")
		search = dataStart
		if at >= 3 && string(raw[at-3:at]) == "end" {
			continue
		}
		if dataStart < len(raw) && raw[dataStart] == '\r' {
			dataStart++
		}
		if dataStart < len(raw) && raw[dataStart] == '\n' {
			dataStart++
		}
		end := bytes.Index(raw[dataStart:], []byte("endstream"))
		if end < 0 {
			break
		}
		dataEnd := dataStart + end
		for dataEnd > dataStart && (raw[dataEnd-1] == '\n' || raw[dataEnd-1] == '\r') {
			dataEnd--
		}
		search = dataStart + end + len("endstream")
		dictStart := bytes.LastIndex(raw[cursor:at], []byte("obj"))
		if dictStart < 0 {
			continue
		}
		dictStart += cursor
		match := pdfStreamLength.FindSubmatchIndex(raw[dictStart:at])
		if match == nil {
			continue
		}
		out.Write(raw[cursor : dictStart+match[2]])
		fmt.Fprintf(&out, "%d", dataEnd-dataStart)
		cursor = dictStart + match[3]
	}
	out.Write(raw[cursor:])
	return out.Bytes()
}
