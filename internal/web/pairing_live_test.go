package web

import (
	"fmt"
	img "image"
	"image/png"
	"os"
	"strings"
	"testing"
)

// rel-1.37.0/W2's decode step, and the shape of the evidence it leaves.
//
// The browser takes the two screenshots — Settings → Phone at full width and at the
// smallest window the frame allows — and this decodes them with the SAME decoder the
// unit case uses, so what is proved is that the QR on screen, at that size, is readable.
//
// AGENTB_QR_SCREENSHOTS names them, semicolon-separated. An entry may carry a crop as
// "file.png@x,y,w,h", because the narrowest-window evidence is a picture of the whole
// window rather than of the element.
func TestTheScreenshotQRsDecode2ns(t *testing.T) {
	list := strings.TrimSpace(os.Getenv("AGENTB_QR_SCREENSHOTS"))
	if list == "" {
		t.Skip("SKIPPED: AGENTB_QR_SCREENSHOTS is not set. The browser writes the screenshots and names them here.")
	}
	for _, entry := range strings.Split(list, ";") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		path, crop, cropped := strings.Cut(entry, "@")
		file, err := os.Open(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		picture, err := png.Decode(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s is not a PNG: %v", path, err)
		}
		if !cropped {
			// A picture of the whole window: the QR is the only WHITE block in this
			// product's six colours, so it is found by looking for white rather than by
			// being told where it is. Measured: coordinates read from the page a moment
			// earlier were stale by the time the picture was taken.
			box, ok := whiteBlock(picture)
			t.Logf("white block in %s: %v (%t)", path, box, ok)
			if ok {
				if sub, can := picture.(interface {
					SubImage(r img.Rectangle) img.Image
				}); can {
					picture = sub.SubImage(box)
				}
			}
		}
		if cropped {
			var x, y, width, height int
			if _, scanErr := fmt.Sscanf(crop, "%d,%d,%d,%d", &x, &y, &width, &height); scanErr != nil {
				t.Fatalf("%s: %v", entry, scanErr)
			}
			sub, ok := picture.(interface {
				SubImage(r img.Rectangle) img.Image
			})
			if !ok {
				t.Fatalf("%s cannot be cropped", path)
			}
			picture = sub.SubImage(img.Rect(x, y, x+width, y+height))
		}
		decoded := decodeQRImage(t, picture)
		if !strings.HasPrefix(decoded, "https://agentb.app/pair#c=") {
			t.Fatalf("%s decodes to %q", path, decoded)
		}
		t.Logf("DECODED %s -> %s", entry, decoded)
	}
}

// whiteBlock is the bounding box of every near-white pixel in the picture. The
// product's palette has no other white, so in a screenshot of the window that box IS the
// QR — including its quiet zone, which is what a reader wants. Taking the longest white
// RUN instead found one row of the quiet zone and nothing else; measured.
func whiteBlock(picture img.Image) (img.Rectangle, bool) {
	bounds := picture.Bounds()
	white := func(x, y int) bool {
		r, g, b, _ := picture.At(x, y).RGBA()
		// Near-PURE white: the product's ink is #D8DDE3, which a looser threshold caught,
		// and the box then covered the heading as well as the code.
		return r > 0xf000 && g > 0xf000 && b > 0xf000
	}
	left, top := bounds.Max.X, bounds.Max.Y
	right, bottom := bounds.Min.X, bounds.Min.Y
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if !white(x, y) {
				continue
			}
			if x < left {
				left = x
			}
			if x > right {
				right = x
			}
			if y < top {
				top = y
			}
			if y > bottom {
				bottom = y
			}
		}
	}
	if right-left < 40 || bottom-top < 40 {
		return img.Rectangle{}, false
	}
	return img.Rect(left, top, right+1, bottom+1), true
}
