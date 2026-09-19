package contextmgr

import "testing"

// v0.69.0/W12 cold review: an id too long to be one this harness minted is not
// read as a number, so it cannot wrap int64 into the re-mint floor.
func TestIDNumberRefusesDigitsThatWouldWrap(t *testing.T) {
	for id, want := range map[string]bool{"m-12": true, "m-999999999999999": true, "m-9223372036854775808": false, "m-99999999999999999999": false} {
		value, ok := idNumber(id)
		if ok != want || value < 0 {
			t.Errorf("idNumber(%q) = %d, %t; want ok=%t and never negative", id, value, ok, want)
		}
	}
}
