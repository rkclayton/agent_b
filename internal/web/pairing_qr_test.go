package web

import (
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"harness/internal/broker"
)

// Item 2ns (b) and (c): the QR is the pairing display, and the link it carries is
// never stored and never logged.
//
// THE QR IS DECODED, NOT COMPARED. The acceptance asks for a decoder rather than an eye
// or a re-encode, so this reads the image back the way a phone does: find the modules,
// read the format information, unmask, and pull the byte-mode payload out of the data
// stream. If the encoder ever produced something a reader could not read, a re-encode
// comparison would agree with itself and say nothing.

func decodeQRPNG(t *testing.T, dataURI string) string {
	t.Helper()
	payload, found := strings.CutPrefix(dataURI, "data:image/png;base64,")
	if !found {
		t.Fatalf("not a PNG data URI: %.40s", dataURI)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("the QR is not a PNG: %v", err)
	}
	return decodeQRImage(t, decoded)
}

// decodeQRImage reads a QR code out of an image. It handles what this product emits —
// byte mode, one segment, no ECI — which is all a reader of this link needs.
func decodeQRImage(t *testing.T, img image.Image) string {
	t.Helper()
	bounds := img.Bounds()
	dark := func(x, y int) bool {
		r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		return r+g+b < 3*0x8000
	}
	// The dark area's bounding box is the code; the quiet zone around it is light. The
	// modules are sampled by PROPORTION rather than by an integer scale, because a
	// screenshot of the page has resized the image and a module is no longer a whole
	// number of pixels — measured, on a browser screenshot whose finder run came out as
	// one pixel under the integer assumption.
	// The code's bounding box, found by looking for RUNS rather than single dark pixels:
	// a screenshot of the page carries a hairline edge around the element, and a
	// bounding box that counted one dark pixel took the whole image — measured, on the
	// first screenshot this case ever decoded.
	// FINDING THE CODE IS A SEARCH, not a measurement. A screenshot of the page carries
	// an antialiased edge where the element meets the page behind it, and at these
	// scales a module is only two or three pixels, so no single erosion rule separates
	// the two — measured, repeatedly, on the narrow shot. So the box and the size are
	// searched together: trim a few pixels, try every legal size, and take the first
	// pair that puts a finder pattern in all three corners. A wrong pair cannot.
	finderAt := func(at func(int, int) bool, originX, originY, size int) bool {
		for row := 0; row < 7; row++ {
			for column := 0; column < 7; column++ {
				edge := row == 0 || row == 6 || column == 0 || column == 6
				core := row >= 2 && row <= 4 && column >= 2 && column <= 4
				if at(originX+column, originY+row) != (edge || core) {
					return false
				}
			}
		}
		return true
	}
	var module func(int, int) bool
	size := 0
	for trim := 0; trim <= 6 && size == 0; trim++ {
		left, top := trim, trim
		right, bottom := bounds.Dx()-1-trim, bounds.Dy()-1-trim
		// Then shrink to the dark content inside that, which removes the quiet zone.
		for top <= bottom && !rowHasDark(img, bounds, top) {
			top++
		}
		for bottom > top && !rowHasDark(img, bounds, bottom) {
			bottom--
		}
		for left <= right && !columnHasDark(img, bounds, left) {
			left++
		}
		for right > left && !columnHasDark(img, bounds, right) {
			right--
		}
		width := float64(right - left + 1)
		height := float64(bottom - top + 1)
		if width < 21 || height < 21 {
			continue
		}
		for candidate := 21; candidate <= 57; candidate += 4 {
			at := func(x, y int) bool {
				return dark(left+int((float64(x)+0.5)*width/float64(candidate)), top+int((float64(y)+0.5)*height/float64(candidate)))
			}
			if finderAt(at, 0, 0, candidate) && finderAt(at, candidate-7, 0, candidate) && finderAt(at, 0, candidate-7, candidate) {
				size, module = candidate, at
				break
			}
		}
	}
	if size == 0 {
		t.Fatal("no trim and size put a finder pattern in all three corners")
	}

	// The mask is READ BY TRYING, not by decoding the format information: there are
	// eight of them, and exactly one produces a byte-mode segment whose text is the
	// link. That is a decode, and it is one fewer place for this decoder to be subtly
	// wrong about a field it does not otherwise need.
	reserved := reservedModules(size)
	// Byte mode carries an 8-bit length below version 10 and 16 bits from version 10.
	lengthBits := 8
	if size >= 57 {
		lengthBits = 16
	}
	for mask := 0; mask < 8; mask++ {
		bits := make([]int, 0, size*size)
		column := size - 1
		upward := true
		for column > 0 {
			if column == 6 {
				column-- // the vertical timing pattern is skipped entirely
			}
			for step := 0; step < size; step++ {
				row := step
				if upward {
					row = size - 1 - step
				}
				for _, x := range []int{column, column - 1} {
					if reserved[row][x] {
						continue
					}
					bits = append(bits, boolBit(unmask(module(x, row), mask, x, row)))
				}
			}
			column -= 2
			upward = !upward
		}
		read := func(count int) int {
			value := 0
			for index := 0; index < count && len(bits) > 0; index++ {
				value = value<<1 | bits[0]
				bits = bits[1:]
			}
			return value
		}
		// THE CODEWORDS ARE INTERLEAVED once a version has more than one
		// error-correction block, so reading the stream straight through gives
		// nonsense. Measured: version 1 and 3 decoded and version 5 did not, and the
		// difference is that version 5 at level M has two blocks.
		data := deInterleave(bits, (size-17)/4)
		if len(data) == 0 {
			continue
		}
		stream := data
		read = func(count int) int {
			value := 0
			for index := 0; index < count && len(stream) > 0; index++ {
				value = value<<1 | stream[0]
				stream = stream[1:]
			}
			return value
		}
		if read(4) != 4 {
			continue
		}
		length := read(lengthBits)
		if length <= 0 || length > size*size/8 {
			continue
		}
		out := make([]byte, 0, length)
		for index := 0; index < length; index++ {
			out = append(out, byte(read(8)))
		}
		if strings.HasPrefix(string(out), "https://") {
			return string(out)
		}
	}
	t.Fatalf("no mask produced a readable byte-mode segment in a %d-module code", size)
	return ""
}

// unmask applies the QR mask pattern the way a reader does.
func unmask(value bool, mask, x, y int) bool {
	flip := false
	switch mask {
	case 0:
		flip = (x+y)%2 == 0
	case 1:
		flip = y%2 == 0
	case 2:
		flip = x%3 == 0
	case 3:
		flip = (x+y)%3 == 0
	case 4:
		flip = (y/2+x/3)%2 == 0
	case 5:
		flip = (x*y)%2+(x*y)%3 == 0
	case 6:
		flip = ((x*y)%2+(x*y)%3)%2 == 0
	case 7:
		flip = ((x+y)%2+(x*y)%3)%2 == 0
	}
	if flip {
		return !value
	}
	return value
}

// deInterleave puts the data codewords back in order. A QR symbol above a certain size
// splits its data into error-correction blocks and then interleaves the codewords of
// every block, so the first codeword of block 1 is followed by the first of block 2 and
// so on. These two tables are level M, versions 1 to 10 — the only sizes this link can
// produce — read from the QR specification.
func deInterleave(bits []int, version int) []int {
	if version < 1 || version > 10 {
		return nil
	}
	dataCodewords := []int{16, 28, 44, 64, 86, 108, 124, 154, 182, 216}[version-1]
	blocks := []int{1, 1, 1, 2, 2, 4, 4, 4, 5, 5}[version-1]
	words := make([]byte, 0, len(bits)/8)
	for index := 0; index+8 <= len(bits); index += 8 {
		value := 0
		for offset := 0; offset < 8; offset++ {
			value = value<<1 | bits[index+offset]
		}
		words = append(words, byte(value))
	}
	if len(words) < dataCodewords {
		return nil
	}
	// The blocks differ in length by at most one codeword; the shorter ones come first.
	short := dataCodewords / blocks
	longBlocks := dataCodewords % blocks
	sizes := make([]int, blocks)
	for index := range sizes {
		sizes[index] = short
		if index >= blocks-longBlocks {
			sizes[index] = short + 1
		}
	}
	ordered := make([][]byte, blocks)
	at := 0
	for round := 0; round < short+1; round++ {
		for block := 0; block < blocks; block++ {
			if round >= sizes[block] || at >= len(words) {
				continue
			}
			ordered[block] = append(ordered[block], words[at])
			at++
		}
	}
	out := make([]int, 0, dataCodewords*8)
	for _, block := range ordered {
		for _, word := range block {
			for offset := 7; offset >= 0; offset-- {
				out = append(out, int(word>>offset)&1)
			}
		}
	}
	return out
}

func boolBit(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rowHasDark(img image.Image, bounds image.Rectangle, y int) bool {
	for x := 0; x < bounds.Dx(); x++ {
		r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		if r+g+b < 3*0x8000 {
			return true
		}
	}
	return false
}

func columnHasDark(img image.Image, bounds image.Rectangle, x int) bool {
	for y := 0; y < bounds.Dy(); y++ {
		r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
		if r+g+b < 3*0x8000 {
			return true
		}
	}
	return false
}

// reservedModules marks the finders, timing, alignment, format and version areas, which
// carry no data.
func reservedModules(size int) [][]bool {
	grid := make([][]bool, size)
	for index := range grid {
		grid[index] = make([]bool, size)
	}
	mark := func(x, y, width, height int) {
		for row := y; row < y+height; row++ {
			for column := x; column < x+width; column++ {
				if row >= 0 && row < size && column >= 0 && column < size {
					grid[row][column] = true
				}
			}
		}
	}
	mark(0, 0, 9, 9)
	mark(size-8, 0, 8, 9)
	mark(0, size-8, 9, 8)
	for index := 0; index < size; index++ {
		grid[6][index] = true
		grid[index][6] = true
	}
	version := (size - 17) / 4
	if version >= 2 {
		centres := alignmentCentres(version, size)
		for _, y := range centres {
			for _, x := range centres {
				if (x < 9 && y < 9) || (x > size-10 && y < 9) || (x < 9 && y > size-10) {
					continue
				}
				mark(x-2, y-2, 5, 5)
			}
		}
	}
	if version >= 7 {
		mark(size-11, 0, 3, 6)
		mark(0, size-11, 6, 3)
	}
	return grid
}

func alignmentCentres(version, size int) []int {
	if version < 2 {
		return nil
	}
	count := version/7 + 2
	first := 6
	last := size - 7
	if count == 2 {
		return []int{first, last}
	}
	step := (last - first + count - 2) / (count - 1)
	step = ((step + 1) / 2) * 2
	centres := []int{first}
	for index := count - 2; index >= 0; index-- {
		centres = append(centres, last-index*step)
	}
	return centres
}

// (b): what the page is given decodes to exactly the document's link.
func TestThePairingQRDecodesToTheLink2ns(t *testing.T) {
	key := make([]byte, 32)
	for index := range key {
		key[index] = byte(index * 7)
	}
	code := "ABCDE-FGHJK-MNPQR-STVWX-YZ012-34567-89ABC-DEFGH"
	link, image, err := pairingQR(code, key)
	if err != nil {
		t.Fatal(err)
	}
	if link != broker.PairingLink(code, key) {
		t.Fatalf("the link is %q", link)
	}
	decoded := decodeQRPNG(t, image)
	if decoded != link {
		t.Fatalf("the QR decodes to\n  %q\nwant\n  %q", decoded, link)
	}
	// And the code is recoverable from what was decoded, which is the phone's whole job.
	readCode, readKey, err := broker.ReadPairingLink(decoded)
	if err != nil {
		t.Fatal(err)
	}
	if readCode != code || string(readKey) != string(key) {
		t.Fatalf("the phone would read code %q", readCode)
	}
	// A key of the wrong size never becomes a QR at all.
	if _, _, err := pairingQR(code, key[:16]); err == nil {
		t.Fatal("a 16-byte key was encoded into a pairing link")
	}
}

// (c): THE LINK IS NEVER STORED OR LOGGED. This searches everything the product writes
// under a data root after a pairing display, for the link, the code and the fragment.
func TestThePairingLinkIsNeverStoredOrLogged2ns(t *testing.T) {
	root, err := os.MkdirTemp("", "agentb-pairing-link-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })

	key := make([]byte, 32)
	code := "QRSTU-VWXYZ-01234-56789-ABCDE-FGHJK-MNPQR-STVWX"
	link, image, err := pairingQR(code, key)
	if err != nil {
		t.Fatal(err)
	}
	if image == "" {
		t.Fatal("no QR was produced")
	}
	// Whatever the product has written under the root, none of it carries any of this.
	needles := []string{link, code, strings.TrimPrefix(link, "https://agentb.app/pair#")}
	var found []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, needle := range needles {
			if strings.Contains(string(body), needle) {
				found = append(found, path)
			}
		}
		return nil
	})
	if len(found) > 0 {
		t.Fatalf("the pairing link reached disk: %v", found)
	}
	// And the source carries no logging of it: the one place the link exists is the
	// response body that answers the page while the code is live.
	source, err := os.ReadFile("broker_endpoint.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		if !strings.Contains(line, "log.") {
			continue
		}
		if strings.Contains(line, "link") || strings.Contains(line, "Code") || strings.Contains(line, "offer") {
			t.Fatalf("the endpoint logs something about the pairing: %s", strings.TrimSpace(line))
		}
	}
}
