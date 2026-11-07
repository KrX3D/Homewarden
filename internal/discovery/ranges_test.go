package discovery

import (
	"reflect"
	"testing"
)

func TestSplitRanges(t *testing.T) {
	got := SplitRanges("192.168.90.0/24, 192.168.20.0/24\n10.0.0.0/30;  ")
	want := []string{"192.168.90.0/24", "192.168.20.0/24", "10.0.0.0/30"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestExpand(t *testing.T) {
	hosts, err := Expand([]string{"10.0.0.0/30", "10.0.0.0/29"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hosts) != 6 { // /29 has 6 usable, /30 overlaps
		t.Fatalf("got %d hosts", len(hosts))
	}
	if _, err := Expand(nil); err == nil {
		t.Fatal("empty list accepted")
	}
	if _, err := Expand([]string{"10.0.0.0/21"}); err == nil {
		t.Fatal("too large range accepted")
	}
	if _, err := Expand([]string{"10.0.0.0/22", "10.1.0.0/22", "10.2.0.0/22", "10.3.0.0/22", "10.4.0.0/24"}); err == nil {
		t.Fatal("total limit not enforced")
	}
}
