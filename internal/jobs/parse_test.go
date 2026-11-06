package jobs

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("", "b") != "b" || firstNonEmpty("a", "b") != "a" {
		t.Fatal("firstNonEmpty")
	}
}
