//go:build windows

package ocr

import (
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

var (
	roActivateInstance    = combase.NewProc("RoActivateInstance")
	iidPdfDocumentStatics = guid{0x433a0b5f, 0xc007, 0x4788, [8]byte{0x90, 0xf2, 0x08, 0x14, 0x3d, 0x92, 0x25, 0x99}}
)

// ExtractPDF renders each page of a PDF with the Windows inbox PDF renderer
// (Windows.Data.Pdf) and recognizes it with the same OCR engine as images
// (item 2fj): the route for a scanned PDF, one with no text layer. Pages are
// joined under "## Page n" headings; a page with no text is skipped, and only
// the first maxPages pages are read.
func ExtractPDF(path string, maxPages int) (string, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hr, _, _ := roInitialize.Call(1) // RO_INIT_MULTITHREADED
	if failed(hr) {
		return "", fmt.Errorf("initialize Windows Runtime: HRESULT 0x%08x", uint32(hr))
	}
	defer roUninitialize.Call()
	engine, err := newEngine()
	if err != nil {
		return "", err
	}
	defer release(engine)

	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	wide, err := syscall.UTF16PtrFromString(absolute)
	if err != nil {
		return "", err
	}
	var file *inspectable
	hr, _, _ = createRandomAccessOnFile.Call(uintptr(unsafe.Pointer(wide)), 0, uintptr(unsafe.Pointer(&iidRandomAccessStream)), uintptr(unsafe.Pointer(&file)))
	if failed(hr) {
		return "", fmt.Errorf("open PDF for OCR: HRESULT 0x%08x", uint32(hr))
	}
	defer release(file)

	statics, err := activationFactory("Windows.Data.Pdf.PdfDocument", &iidPdfDocumentStatics)
	if err != nil {
		return "", err
	}
	defer release(statics)
	// IPdfDocumentStatics: LoadFromFileAsync 6, LoadFromFileWithPasswordAsync 7,
	// LoadFromStreamAsync 8.
	var loading *inspectable
	hr, _, _ = syscall.SyscallN(statics.vtable[8], uintptr(unsafe.Pointer(statics)), uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(&loading)))
	if failed(hr) {
		return "", fmt.Errorf("load PDF for OCR: HRESULT 0x%08x", uint32(hr))
	}
	defer release(loading)
	document, err := asyncResult(loading)
	if err != nil {
		return "", err
	}
	defer release(document)
	// IPdfDocument: GetPage 6, get_PageCount 7.
	var count uint32
	hr, _, _ = syscall.SyscallN(document.vtable[7], uintptr(unsafe.Pointer(document)), uintptr(unsafe.Pointer(&count)))
	if failed(hr) {
		return "", fmt.Errorf("count PDF pages: HRESULT 0x%08x", uint32(hr))
	}
	var output strings.Builder
	for index := uint32(0); index < count && int(index) < maxPages; index++ {
		text, pageErr := recognizePDFPage(engine, document, index)
		if errors.Is(pageErr, ErrNoText) {
			continue
		}
		if pageErr != nil {
			return "", fmt.Errorf("page %d: %w", index+1, pageErr)
		}
		fmt.Fprintf(&output, "## Page %d\n\n%s\n\n", index+1, text)
	}
	if output.Len() == 0 {
		return "", ErrNoText
	}
	if int(count) > maxPages {
		fmt.Fprintf(&output, "[OCR stopped after %d of %d pages]\n", maxPages, count)
	}
	return output.String(), nil
}

// recognizePDFPage renders one page to an in-memory PNG stream and recognizes it.
func recognizePDFPage(engine, document *inspectable, index uint32) (string, error) {
	var page *inspectable
	hr, _, _ := syscall.SyscallN(document.vtable[6], uintptr(unsafe.Pointer(document)), uintptr(index), uintptr(unsafe.Pointer(&page)))
	if failed(hr) {
		return "", fmt.Errorf("get PDF page: HRESULT 0x%08x", uint32(hr))
	}
	defer release(page)
	memory, err := activateInstance("Windows.Storage.Streams.InMemoryRandomAccessStream")
	if err != nil {
		return "", err
	}
	defer release(memory)
	stream, err := query(memory, &iidRandomAccessStream)
	if err != nil {
		return "", err
	}
	defer release(stream)
	// IPdfPage: RenderToStreamAsync 6 (PNG at the page's own size).
	var rendering *inspectable
	hr, _, _ = syscall.SyscallN(page.vtable[6], uintptr(unsafe.Pointer(page)), uintptr(unsafe.Pointer(stream)), uintptr(unsafe.Pointer(&rendering)))
	if failed(hr) {
		return "", fmt.Errorf("render PDF page: HRESULT 0x%08x", uint32(hr))
	}
	defer release(rendering)
	if _, err := asyncResult(rendering); err != nil {
		return "", err
	}
	// IRandomAccessStream: Seek 11.
	hr, _, _ = syscall.SyscallN(stream.vtable[11], uintptr(unsafe.Pointer(stream)), 0)
	if failed(hr) {
		return "", fmt.Errorf("rewind rendered page: HRESULT 0x%08x", uint32(hr))
	}
	return recognizeStream(engine, stream)
}

func activateInstance(name string) (*inspectable, error) {
	encoded := utf16.Encode([]rune(name))
	var className uintptr
	hr, _, _ := windowsCreateString.Call(uintptr(unsafe.Pointer(&encoded[0])), uintptr(len(encoded)), uintptr(unsafe.Pointer(&className)))
	if failed(hr) {
		return nil, fmt.Errorf("create Windows Runtime class name: HRESULT 0x%08x", uint32(hr))
	}
	defer windowsDeleteString.Call(className)
	var instance *inspectable
	hr, _, _ = roActivateInstance.Call(className, uintptr(unsafe.Pointer(&instance)))
	if failed(hr) {
		return nil, fmt.Errorf("activate %s: HRESULT 0x%08x", name, uint32(hr))
	}
	return instance, nil
}
