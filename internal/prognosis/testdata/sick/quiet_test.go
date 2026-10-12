package sick

import "testing"

func TestKnotty(t *testing.T) {
	if Knotty(0) != 1 {
		t.Fatal("Knotty(0)")
	}
}
