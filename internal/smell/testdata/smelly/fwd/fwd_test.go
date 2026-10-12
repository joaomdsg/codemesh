package fwd

import "testing"

func TestRender(t *testing.T) {
	if render("a", false, 1) != 1 {
		t.Fail()
	}
}
