// main_test.go — unit tests for the pure helpers in the CHO-2247 backfill
// sweep. The DB-bound sweep (runDryRun / runCanary / runSweep / loadCourses /
// emitOne) and main() require a live Cloud SQL pool — composition-root
// plateau; the SQL-shape + RLS semantics are exercised in
// main_integration_test.go (build tag `integration`).
package main

import "testing"

func TestContainsCourse(t *testing.T) {
	t.Parallel()

	rows := []courseRow{
		{CourseID: "c1"},
		{CourseID: "c2"},
	}

	cases := []struct {
		name string
		id   string
		want bool
	}{
		{"present", "c1", true},
		{"present last", "c2", true},
		{"absent", "c3", false},
		{"empty rows", "", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var rs []courseRow
			if tc.id != "c3" && tc.name != "absent" {
				rs = rows
			}
			if got := containsCourse(rs, tc.id); got != tc.want {
				t.Fatalf("containsCourse(%v, %q) = %v, want %v", rs, tc.id, got, tc.want)
			}
		})
	}
}

func TestSplitNonEmpty(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"single", "a", []string{"a"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trims + drops empties", " a ,,b, ,c ", []string{"a", "b", "c"}},
		{"all empties", ", ,", nil},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitNonEmpty(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitNonEmpty(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("splitNonEmpty(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
		})
	}
}
