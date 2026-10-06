package hermes

import "testing"

func TestCronjobIsSupported2qs(t *testing.T) {
	for _, name := range unsupported {
		if name == "cronjob" {
			t.Fatal("cronjob remains on the Hermes unsupported list")
		}
	}
}
