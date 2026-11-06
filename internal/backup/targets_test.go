package backup

import (
	"context"
	"testing"
)

func TestSafeRel(t *testing.T) {
	for _, bad := range []string{"", "..", "../x", "/../../etc/passwd"} {
		if r, err := safeRel(bad); err == nil && (r == "" || r[0] == '.') {
			t.Fatalf("%q accepted as %q", bad, r)
		}
	}
	if r, _ := safeRel("../../etc/passwd"); r != "etc/passwd" {
		t.Fatalf("traversal must be clamped inside the target, got %q", r)
	}
}

func TestLocalTarget(t *testing.T) {
	l := &local{root: t.TempDir()}
	if err := Test(context.Background(), l); err != nil {
		t.Fatal(err)
	}
}
