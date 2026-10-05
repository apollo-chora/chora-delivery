// coverage_test.go — closes the remaining statement gaps in rostering.go.
// sortStrings is package-private, so this file lives in the internal test
// package to drive its swap branch deterministically (the black-box List() path
// iterates a map in unspecified order and cannot guarantee an inversion).
package rostering

import (
	"reflect"
	"testing"
)

func TestSortStrings_UnsortedAndNil(t *testing.T) {
	// A fully-unsorted input forces both the inner loop and the swap.
	in := []string{"zulu", "alpha", "mike", "bravo"}
	sortStrings(in)
	want := []string{"alpha", "bravo", "mike", "zulu"}
	if !reflect.DeepEqual(in, want) {
		t.Fatalf("sortStrings: got %v want %v", in, want)
	}

	// Nil and single-element slices are no-ops (bounds of the outer loop).
	sortStrings(nil)
	sortStrings([]string{"solo"})

	// Already-sorted input leaves the swap untaken but stays stable.
	sorted := []string{"a", "b", "c"}
	sortStrings(sorted)
	if !reflect.DeepEqual(sorted, []string{"a", "b", "c"}) {
		t.Fatalf("sortStrings on sorted input mutated it: %v", sorted)
	}
}
