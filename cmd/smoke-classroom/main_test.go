// main_test.go — unit tests for the pure helpers in the classroom smoke
// probe. The network probe itself (main / do / dialWSAuthed / dialWS / recv /
// probeCrossPod / probeCrossPodPoll) requires a live cluster + WS endpoints —
// composition-root plateau. must() calls os.Exit(1) on failure and is not
// unit-testable directly.
package main

import "testing"

func TestStr(t *testing.T) {
	t.Parallel()

	if got := str("hello"); got != "hello" {
		t.Fatalf("str(string) = %q, want hello", got)
	}
	if got := str(42); got != "" {
		t.Fatalf("str(int) = %q, want empty (type mismatch)", got)
	}
	if got := str(nil); got != "" {
		t.Fatalf("str(nil) = %q, want empty", got)
	}
}

func TestNum(t *testing.T) {
	t.Parallel()

	if got := num(float64(3.5)); got != 3.5 {
		t.Fatalf("num(float64) = %v, want 3.5", got)
	}
	if got := num("3.5"); got != 0 {
		t.Fatalf("num(string) = %v, want 0 (non-float64 type)", got)
	}
}
