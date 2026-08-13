package native

import (
	"strings"
	"testing"
)

// stream feeds deltas through a redactor the way the SSE loop does, then flushes.
func stream(deltas ...string) string {
	var r idRedactor
	var out strings.Builder
	for _, d := range deltas {
		out.WriteString(r.push(d))
	}
	out.WriteString(r.flush())
	return out.String()
}

func TestRedactsShapeIDFromProse(t *testing.T) {
	got := stream(`The box shape:HAxxVP1i is empty.`)
	if strings.Contains(got, "shape:") {
		t.Errorf("id survived: %q", got)
	}
	if !strings.Contains(got, "The box") || !strings.Contains(got, "is empty.") {
		t.Errorf("surrounding prose was damaged: %q", got)
	}
}

// The hard case: an id split across two deltas, which is normal when a model
// streams token by token.
func TestRedactsIDSplitAcrossDeltas(t *testing.T) {
	cases := [][]string{
		{"The box ", "shape:", "aB3", " is empty."},
		{"The box sh", "ape:aB3 is empty."},
		{"The box shape:aB", "3 is empty."},
		{"The box shape", ":aB3", " is empty."},
	}
	for _, deltas := range cases {
		got := stream(deltas...)
		if strings.Contains(got, "shape:") {
			t.Errorf("%v -> id survived: %q", deltas, got)
		}
		if !strings.Contains(got, "empty") {
			t.Errorf("%v -> text lost: %q", deltas, got)
		}
	}
}

func TestRemovesParentheticalIDs(t *testing.T) {
	for _, in := range []string{
		`the auth service (shape:x7Kq2) talks to it`,
		`the auth service (id: shape:x7Kq2) talks to it`,
	} {
		got := stream(in)
		if strings.Contains(got, "shape:") || strings.Contains(got, "()") {
			t.Errorf("%q -> %q", in, got)
		}
		if !strings.Contains(got, "the auth service") || !strings.Contains(got, "talks to it") {
			t.Errorf("prose damaged: %q -> %q", in, got)
		}
	}
}

func TestLeavesOrdinaryTextAlone(t *testing.T) {
	in := "The tea flow has seven steps, ending at Serve. Nothing is missing."
	if got := stream(in); got != in {
		t.Errorf("text was altered:\n got %q\nwant %q", got, in)
	}
}

// "shape" as an English word must not be eaten.
func TestDoesNotEatTheWordShape(t *testing.T) {
	for _, in := range []string{
		"The shape on the left is a box.",
		"Every shape has a label.",
		"That shape: a rectangle.",
	} {
		got := stream(in)
		if !strings.Contains(got, "shape") {
			t.Errorf("%q -> lost the word: %q", in, got)
		}
	}
}

func TestFlushEmitsHeldFragment(t *testing.T) {
	// A trailing "shape" is held back mid-stream but must still be delivered.
	var r idRedactor
	mid := r.push("Look at the shape")
	tail := r.flush()
	if strings.Contains(mid, "shape") {
		t.Errorf("fragment should have been held: %q", mid)
	}
	if got := mid + tail; got != "Look at the shape" {
		t.Errorf("got %q", got)
	}
}

func TestRedactsMultipleIDs(t *testing.T) {
	got := stream("shape:aaa points at shape:bbb and shape:ccc")
	if strings.Contains(got, "shape:") {
		t.Errorf("ids survived: %q", got)
	}
	if !strings.Contains(got, "points at") {
		t.Errorf("prose damaged: %q", got)
	}
}

// Regression: the id and its brackets arrived in different deltas, so neither
// chunk could see that removing the id had left "()" behind.
func TestParentheticalIDSplitAcrossDeltasLeavesNoEmptyParens(t *testing.T) {
	cases := [][]string{
		{"the box on the left (", "shape:aB3", ") is connected"},
		{"the box on the left (shape:aB3", ") is connected"},
		{"the box on the left ", "(shape:aB3) is connected"},
		{"the box (", "id: ", "shape:aB3", ") there"},
	}
	for _, deltas := range cases {
		got := stream(deltas...)
		if strings.Contains(got, "()") || strings.Contains(got, "( )") {
			t.Errorf("%v -> empty parens survived: %q", deltas, got)
		}
		if strings.Contains(got, "shape:") {
			t.Errorf("%v -> id survived: %q", deltas, got)
		}
		if !strings.Contains(got, "connected") && !strings.Contains(got, "there") {
			t.Errorf("%v -> prose lost: %q", deltas, got)
		}
	}
}

// A long parenthetical with no id must still stream rather than be held forever.
func TestLongParentheticalIsNotHeldIndefinitely(t *testing.T) {
	long := "(" + strings.Repeat("a very long aside ", 20)
	var r idRedactor
	out := r.push("text " + long)
	if out == "" {
		t.Error("a parenthetical with no id should not be buffered wholesale")
	}
}
