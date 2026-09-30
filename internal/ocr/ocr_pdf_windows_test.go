//go:build windows

package ocr

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scannedPDF writes a PDF whose only page content is a JPEG of image —
// what a scanner produces, with no text layer.
func scannedPDF(t *testing.T, imagePath string, pages int) string {
	t.Helper()
	file, err := os.Open(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	picture, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, picture, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	width, height := picture.Bounds().Dx(), picture.Bounds().Dy()
	content := fmt.Sprintf("q %d 0 0 %d 0 0 cm /Im1 Do Q", width, height)
	objects := []string{
		"<</Type/Catalog/Pages 2 0 R>>",
		"",
	}
	kids := make([]string, 0, pages)
	imageObject := 3 + pages*2
	for index := 0; index < pages; index++ {
		pageObject := 3 + index*2
		kids = append(kids, fmt.Sprintf("%d 0 R", pageObject))
		objects = append(objects, fmt.Sprintf("<</Type/Page/Parent 2 0 R/MediaBox[0 0 %d %d]/Contents %d 0 R/Resources<</XObject<</Im1 %d 0 R>>>>>>", width, height, pageObject+1, imageObject), fmt.Sprintf("<</Length %d>>stream\n%s\nendstream", len(content), content))
	}
	objects[1] = fmt.Sprintf("<</Type/Pages/Kids[%s]/Count %d>>", strings.Join(kids, " "), pages)
	objects = append(objects, fmt.Sprintf("<</Type/XObject/Subtype/Image/Width %d/Height %d/ColorSpace/DeviceRGB/BitsPerComponent 8/Filter/DCTDecode/Length %d>>stream\n%s\nendstream", width, height, encoded.Len(), encoded.String()))
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{}
	for index, object := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	start := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, offset := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root 1 0 R>>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, start)
	path := filepath.Join(t.TempDir(), "scanned.pdf")
	if err := os.WriteFile(path, out.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Item 2fj: a scanned PDF goes through the inbox OCR page by page.
func TestAScannedPDFIsReadByOCRPageByPage(t *testing.T) {
	path := scannedPDF(t, filepath.Join("testdata", "setup-screen.png"), 60)
	text, pages, total, stopped, err := ExtractPDF(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("scanned PDF OCR: %v", err)
	}
	if pages != 60 || total != 60 || stopped || strings.Count(text, "## Page ") != 60 || strings.Contains(text, "truncated") || !strings.Contains(text, "Agent_b Setup") || !strings.Contains(text, "reach its models") {
		t.Fatalf("OCR text: %q", text)
	}
	ctx, cancel := context.WithCancel(context.Background())
	text, pages, total, stopped, err = ExtractPDF(ctx, path, func(page, total int) {
		if page == 10 {
			cancel()
		}
	})
	if err != nil || pages != 10 || total != 60 || !stopped || strings.Count(text, "## Page ") != 10 {
		t.Fatalf("stopped OCR pages=%d total=%d stopped=%v err=%v text=%q", pages, total, stopped, err, text)
	}
}
