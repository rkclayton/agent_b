//go:build windows

package main

import (
	"bytes"
	"log"
	"strings"
	"testing"
	"time"
)

func TestHostHitTestOwnsEveryResizeEdgeAndCorner(t *testing.T) {
	const width, height, border = int32(1280), int32(860), int32(8)
	cases := []struct {
		name string
		x, y int32
		want uintptr
	}{
		{"top", 640, 2, htTop}, {"bottom", 640, 858, htBottom},
		{"left", 2, 430, htLeft}, {"right", 1278, 430, htRight},
		{"top left", 2, 2, htTopLeft}, {"top right", 1278, 2, htTopRight},
		{"bottom left", 2, 858, htBottomLeft}, {"bottom right", 1278, 858, htBottomRight},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if got := hostHitTest(test.x, test.y, width, height, border, border, false, 96); got != test.want {
				t.Fatalf("hit=%d want %d", got, test.want)
			}
		})
	}
}

func TestHostHitTestUsesCompactCaptionControls(t *testing.T) {
	const width, height, border = int32(1280), int32(860), int32(8)
	contentRight := width - border
	start := contentRight - int32(hostButtonWidth*hostButtonCount)
	for index, want := range []uintptr{htMinButton, htMaxButton, htClose} {
		x := start + int32(index*hostButtonWidth+hostButtonWidth/2)
		if got := hostHitTest(x, 16, width, height, border, border, false, 96); got != want {
			t.Fatalf("control %d hit=%d want %d", index, got, want)
		}
	}
	if got := hostHitTest(start-1, 2, width, height, border, border, false, 96); got != htTop {
		t.Fatalf("top resize edge must win over controls, got %d", got)
	}
}

func TestHostHitTestMatchesDrawnCaptionControlsAtEveryScale2se(t *testing.T) {
	const width, height, border = int32(1920), int32(1080), int32(8)
	for _, dpi := range []int32{96, 120, 144, 168, 192} {
		button := scaleHostPixel(hostButtonWidth, dpi)
		contentRight := width - border
		start := contentRight - button*hostButtonCount
		for index, want := range []uintptr{htMinButton, htMaxButton, htClose} {
			for _, x := range []int32{start + int32(index)*button + 1, start + int32(index+1)*button - 1} {
				for _, y := range []int32{1, scaleHostPixel(hostStripHeight, dpi) / 2, scaleHostPixel(hostStripHeight, dpi) - 1} {
					if got := hostHitTest(x, y, width, height, border, border, false, dpi); got != want {
						t.Fatalf("dpi=%d control=%d point=%d,%d hit=%d want=%d", dpi, index, x, y, got, want)
					}
				}
			}
		}
	}
}

func TestBothCaptionRoutesActOnceAndWriteOneCompleteLine2se(t *testing.T) {
	oldWriter, oldFlags := log.Writer(), log.Flags()
	defer log.SetOutput(oldWriter)
	defer log.SetFlags(oldFlags)
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	hostPresses.Lock()
	hostPresses.action, hostPresses.route, hostPresses.at = "", "", time.Time{}
	hostPresses.Unlock()
	if !recordHostPress(0, "maximize", htMaxButton) || recordHostPress(0, "maximize", hostPressPage) {
		t.Fatal("caption and page delivery for one press did not dispatch exactly once")
	}
	line := strings.TrimSpace(output.String())
	for _, field := range []string{"button=maximize", "route=caption", "hit=9", "scale=100", "action=maximize"} {
		if !strings.Contains(line, field) {
			t.Fatalf("log %q lacks %q", line, field)
		}
	}
}

func TestCaptionButtonDownDispatchesTheActionOnItsFirstMessage(t *testing.T) {
	for hit, want := range map[uintptr]uintptr{htMinButton: wmHostMinimize, htMaxButton: wmHostMaximize, htClose: wmHostClose} {
		if got := hostCaptionMessage(hit); got != want {
			t.Fatalf("hit %d dispatched %d, want %d", hit, got, want)
		}
	}
	if got := hostCaptionMessage(htCaption); got != 0 {
		t.Fatalf("caption dispatched %d", got)
	}
}
