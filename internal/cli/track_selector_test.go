package cli

import "testing"

func TestParseSelectionInput(t *testing.T) {
	got := parseSelectionInput("1,3-4,7", 7)
	want := []int{1, 3, 4, 7}
	if len(got) != len(want) {
		t.Fatalf("unexpected selection length: %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected selection at %d: got %#v want %#v", i, got, want)
		}
	}
}

func TestParseSelectionInputAll(t *testing.T) {
	got := parseSelectionInput("all", 3)
	want := []int{1, 2, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("unexpected all selection: %#v", got)
		}
	}
}
