//go:build windows

package main

import "testing"

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
			if got := hostHitTest(test.x, test.y, width, height, border, border, false); got != test.want {
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
		if got := hostHitTest(x, 16, width, height, border, border, false); got != want {
			t.Fatalf("control %d hit=%d want %d", index, got, want)
		}
	}
	if got := hostHitTest(contentRight-1, 2, width, height, border, border, false); got != htTop {
		t.Fatalf("top resize edge must win over controls, got %d", got)
	}
}
