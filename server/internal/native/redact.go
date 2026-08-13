package native

import (
	"regexp"
	"strings"
)

// shapeIDPattern matches a tldraw shape id as it appears in prose. Ids are
// `shape:` followed by an opaque token, so the run of id-ish characters after
// the prefix is the id.
var shapeIDPattern = regexp.MustCompile(`shape:[A-Za-z0-9_\-]+`)

// idRedactor strips shape ids out of streamed assistant text.
//
// The system prompt forbids quoting ids at the person — they are opaque, and
// "the box labeled shape:HAxxVP1i" is meaningless to read. A 30B model follows
// that instruction most of the time but not always, and the rule is absolute, so
// it is enforced here rather than hoped for.
//
// Redaction happens on the way to the UI only. The model's own text goes into
// history unmodified: rewriting what it said would make it argue with itself, and
// it legitimately needs ids for tool arguments.
type idRedactor struct {
	// pending holds a trailing fragment that might be the start of an id split
	// across two deltas ("…the box shape:" | "aB3 is empty").
	pending string
}

// idFragment matches a partial id at the very end of a chunk, which must be held
// back until the next delta completes or refutes it.
//
// An unclosed "(" is held too: the id inside it may be the whole content of the
// parenthetical, and removing the id without its brackets leaves "()" stranded.
// Chunks arrive token by token, so the "(" and ")" routinely land in different
// deltas and neither chunk alone can see that the pair became empty.
var idFragment = regexp.MustCompile(`(?:s|sh|sha|shap|shape|shape:[A-Za-z0-9_\-]*|\([^)]*)$`)

// push accepts a delta and returns the text safe to emit now.
func (r *idRedactor) push(delta string) string {
	buf := r.pending + delta
	r.pending = ""

	// Hold back a trailing fragment that could still grow into an id.
	if loc := idFragment.FindStringIndex(buf); loc != nil {
		r.pending = buf[loc[0]:]
		buf = buf[:loc[0]]
	}
	return clean(buf)
}

// flush returns whatever was held back, at end of stream.
func (r *idRedactor) flush() string {
	out := clean(r.pending)
	r.pending = ""
	return out
}

// clean removes ids and tidies the punctuation they leave behind.
func clean(s string) string {
	if !strings.Contains(s, "shape:") {
		return s
	}
	out := shapeIDPattern.ReplaceAllString(s, "")
	// "(shape:x)" and "(shape:x, shape:y)" are the common shapes of the mistake;
	// removing the id alone would leave "()" or " ()" behind.
	out = emptyParens.ReplaceAllString(out, "")
	out = strings.ReplaceAll(out, "(id: )", "")
	out = strings.ReplaceAll(out, "(id )", "")
	out = spaceBeforePunct.ReplaceAllString(out, "$1")
	out = multiSpace.ReplaceAllString(out, " ")
	return out
}

var (
	emptyParens      = regexp.MustCompile(`\s*\(\s*(?:id\s*:?\s*)?[,\s]*\)`)
	spaceBeforePunct = regexp.MustCompile(`\s+([,.;:!?])`)
	multiSpace       = regexp.MustCompile(`(?m)[ \t]{2,}`)
)
