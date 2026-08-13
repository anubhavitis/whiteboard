// Package docs holds tests that check the published documentation against the
// code it describes.
//
// It exists because the README accumulated seven factual errors in a single
// session — it advertised a database the project does not use, listed a stale
// shape enum, omitted a config variable, and cited decision numbers that had moved
// on. Every one of those was a claim copied from source into prose and never
// re-checked.
//
// The rule these tests enforce: public docs may state behaviour and rationale, but
// may not restate values, enums or schemas that live in code. Where they must name
// something, they name the identifier rather than its value.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/anubhavitis/whiteboard/server/internal/agent"
)

// repoRoot walks up from this package to the repository root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// trackedDocs are the docs published in the repository. Gitignored files
// (DECISIONS.md, planv2.md) are excluded: nobody outside can read them, so they
// are not what these tests are protecting.
func trackedDocs(t *testing.T) map[string]string {
	t.Helper()
	root := repoRoot(t)
	paths := []string{
		"README.md",
		"CLAUDE.md",
		"docs/architecture.md",
		"docs/agent-behaviour.md",
		"server/internal/agent/canvas_skill.md",
	}
	out := map[string]string{}
	for _, p := range paths {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatalf("reading %s: %v", p, err)
		}
		out[p] = string(b)
	}
	return out
}

var envVarPattern = regexp.MustCompile(`WHITEBOARD_[A-Z_]+`)

// Every environment variable main.go reads must be documented, and every variable
// documented must exist. A missing one was error four of seven.
func TestConfigTableMatchesTheCode(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "server/cmd/whiteboard/main.go"))
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}

	inCode := uniqueMatches(envVarPattern, string(src))
	inDocs := uniqueMatches(envVarPattern, string(readme))

	for _, v := range inCode {
		if !contains(inDocs, v) {
			t.Errorf("%s is read by main.go but absent from the README config table", v)
		}
	}
	for _, v := range inDocs {
		if !contains(inCode, v) {
			t.Errorf("%s is documented but no longer read by main.go", v)
		}
	}
}

// The docs must not name a tool that does not exist. A renamed tool would
// otherwise leave the docs quietly describing a capability nobody has.
func TestDocsNameOnlyRealTools(t *testing.T) {
	real := map[string]bool{}
	for _, tl := range agent.Tools() {
		real[tl.Name] = true
	}
	// Tool-shaped identifiers the docs might plausibly use.
	candidate := regexp.MustCompile(`\b(create|update|delete|move|resize|group)_[a-z_]+\b`)

	for path, body := range trackedDocs(t) {
		for _, name := range uniqueMatches(candidate, body) {
			if !real[name] {
				t.Errorf("%s names %q, which is not in agent.Tools()", path, name)
			}
		}
	}
}

// Anything documented as a drawable shape must be in the schema's enum. The shape
// list went stale when `diamond` was added.
func TestDocsNameOnlyRealShapes(t *testing.T) {
	var enum []string
	for _, tl := range agent.Tools() {
		if tl.Name == "create_shape" {
			enum = tl.Schema.Properties["shape"].Enum
		}
	}
	if len(enum) == 0 {
		t.Fatal("create_shape has no shape enum")
	}

	// Only check the canvas skill: it is the file that tells an agent which shapes
	// exist, so a stale list there is a functional bug, not just bad prose.
	skill := trackedDocs(t)["server/internal/agent/canvas_skill.md"]
	for _, s := range enum {
		if !strings.Contains(skill, "`"+s+"`") {
			t.Errorf("shape %q is in the schema but the canvas skill never mentions it", s)
		}
	}
}

// Technologies the project does not use must not appear as though it does. The
// README advertised SQLite from its first commit; nothing ever imported it.
func TestDocsDoNotClaimAbsentDependencies(t *testing.T) {
	root := repoRoot(t)
	goMod, err := os.ReadFile(filepath.Join(root, "server/go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	pkgJSON, err := os.ReadFile(filepath.Join(root, "web/package.json"))
	if err != nil {
		t.Fatal(err)
	}
	deps := string(goMod) + string(pkgJSON)

	// Only the stack line and the config table make dependency claims. Prose
	// elsewhere may legitimately name a technology as an example — "Postgres" is a
	// label on a drawn box in the sample turn, not a claim that we run one — so
	// matching the whole file produces false positives that teach people to
	// weaken the test.
	readme := trackedDocs(t)["README.md"]
	claimZone := stackLineAndConfigTable(readme)

	for _, tech := range []string{"sqlite", "postgres", "redis", "mysql", "ANTHROPIC_API_KEY", "OPENAI_API_KEY"} {
		if strings.Contains(strings.ToLower(deps), strings.ToLower(tech)) {
			continue // we really do depend on it
		}
		if strings.Contains(strings.ToLower(claimZone), strings.ToLower(tech)) {
			t.Errorf("the README's stack line or config table names %q, which this project "+
				"does not depend on", tech)
		}
	}
}

var (
	planRef     = regexp.MustCompile(`planv2|plan\.md`)
	decisionRef = regexp.MustCompile(`\bD[1-9][0-9]?\b`)
)

// Published docs must not cite gitignored files or their decision numbers: a
// reader outside this machine cannot resolve either, so the citation is noise at
// best and misleading at worst.
func TestPublicDocsDoNotCiteUnpublishedFiles(t *testing.T) {
	for path, body := range trackedDocs(t) {
		// CLAUDE.md is for agents working in this checkout, where both files exist.
		if path == "CLAUDE.md" {
			continue
		}
		if loc := planRef.FindString(body); loc != "" {
			t.Errorf("%s cites %q, which is gitignored and unreadable to anyone else", path, loc)
		}
		if loc := decisionRef.FindString(body); loc != "" {
			t.Errorf("%s cites decision %q from the gitignored DECISIONS.md; inline the reason instead",
				path, loc)
		}
	}
}

// Constants must be cited by name, not value. A prose number goes stale silently
// the moment the constant changes; a name does not.
func TestDocsCiteConstantsByNameNotValue(t *testing.T) {
	// The cap is the one most often quoted, and it was quoted wrongly: 15 bounds
	// the native loop only, while Claude Code uses --max-turns.
	banned := []struct{ pattern, why string }{
		{`capped at [0-9]+`, "cite agent.MaxToolCallsPerTurn instead of its value"},
		{`[0-9]+ ?s(econd)? timeout`, "cite ws.toolTimeout instead of its value"},
		{`MaxToolCallsPerTurn \([0-9]+\)`, "the parenthesised value goes stale; the name is enough"},
	}
	for path, body := range trackedDocs(t) {
		for _, b := range banned {
			if m := regexp.MustCompile(b.pattern).FindString(body); m != "" {
				t.Errorf("%s says %q — %s", path, m, b.why)
			}
		}
	}
}

// A doc that names a test is making a promise about the test suite. Renaming the
// test should fail here rather than silently orphan the reference.
func TestNamedTestsExist(t *testing.T) {
	root := repoRoot(t)
	namePattern := regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9]+\b`)

	// Collect every test name in the Go tree.
	existing := map[string]bool{}
	filepath.Walk(filepath.Join(root, "server"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		for _, n := range namePattern.FindAllString(string(b), -1) {
			existing[n] = true
		}
		return nil
	})

	for path, body := range trackedDocs(t) {
		for _, n := range uniqueMatches(namePattern, body) {
			if !existing[n] {
				t.Errorf("%s references %s, which no longer exists", path, n)
			}
		}
	}
}

// Relative links in the docs must resolve, or the split we just did rots into
// dead ends.
func TestDocLinksResolve(t *testing.T) {
	root := repoRoot(t)
	link := regexp.MustCompile(`\]\(([^)#]+\.(?:md|svg))\)`)

	for path, body := range trackedDocs(t) {
		dir := filepath.Dir(filepath.Join(root, path))
		for _, m := range link.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if strings.HasPrefix(target, "http") {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, target)); err != nil {
				t.Errorf("%s links to %s, which does not exist", path, target)
			}
		}
	}
}

// stackLineAndConfigTable returns the parts of the README that assert what the
// project is built on: the **Stack:** paragraph and the configuration table.
func stackLineAndConfigTable(readme string) string {
	var b strings.Builder
	lines := strings.Split(readme, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "**Stack:**") {
			// The stack claim can wrap onto following lines.
			for _, cont := range lines[i:min(i+3, len(lines))] {
				b.WriteString(cont + "\n")
			}
		}
		// Config table rows name a variable and its default.
		if strings.HasPrefix(l, "| `WHITEBOARD_") || strings.HasPrefix(l, "| `VITE_") {
			b.WriteString(l + "\n")
		}
	}
	return b.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func uniqueMatches(re *regexp.Regexp, s string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range re.FindAllString(s, -1) {
		if !seen[m] {
			seen[m] = true
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// Every diagram must ship a -light and a -dark variant, and be embedded through a
// <picture> element.
//
// A single transparent SVG is not enough, which cost a round trip to learn: GitHub
// renders <img src="*.svg"> against a white background of its own, so a file tuned
// for dark mode appears as a white slab there no matter how transparent it is.
// prefers-color-scheme inside the SVG does not help either — GitHub's img context
// does not apply it. Two files selected by <picture> is the only mechanism that
// works.
func TestDiagramsShipBothThemeVariants(t *testing.T) {
	root := repoRoot(t)
	svgs, err := filepath.Glob(filepath.Join(root, "docs", "*.svg"))
	if err != nil || len(svgs) == 0 {
		t.Fatalf("no diagrams found: %v", err)
	}

	bases := map[string]bool{}
	for _, p := range svgs {
		n := strings.TrimSuffix(filepath.Base(p), ".svg")
		n = strings.TrimSuffix(strings.TrimSuffix(n, "-light"), "-dark")
		bases[n] = true
	}

	for base := range bases {
		for _, suffix := range []string{"-light", "-dark"} {
			want := filepath.Join(root, "docs", base+suffix+".svg")
			if _, err := os.Stat(want); err != nil {
				t.Errorf("%s%s.svg is missing; both variants must exist", base, suffix)
			}
		}
	}

	// The dark variant must not carry a light background, and vice versa.
	for _, p := range svgs {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		body, name := string(b), filepath.Base(p)
		if regexp.MustCompile(`<rect x="0"\s+y="0"`).MatchString(body) {
			t.Errorf("%s paints a full-canvas background rect; the theme variant handles that", name)
		}
	}
}

// A diagram embedded with a bare <img> shows the wrong variant in one theme. Every
// embed must go through <picture> with a prefers-color-scheme source.
func TestDiagramsAreEmbeddedViaPicture(t *testing.T) {
	bareImg := regexp.MustCompile(`<img src="[^"]*\.svg"`)
	for path, body := range trackedDocs(t) {
		for _, m := range bareImg.FindAllString(body, -1) {
			if !strings.Contains(m, "-light.svg") {
				t.Errorf("%s embeds %s directly; use <picture> so dark mode gets its variant",
					path, m)
			}
		}
		// Each <picture> needs its dark source.
		pics := strings.Count(body, "<picture>")
		darks := strings.Count(body, "prefers-color-scheme: dark")
		if pics != darks {
			t.Errorf("%s has %d <picture> blocks but %d dark sources", path, pics, darks)
		}
	}
}

// A diagram is only useful if it is reachable. Every theme variant must be
// embedded, and every base must be listed in the generator that produces those
// variants — otherwise it is dead weight nobody will maintain.
func TestEveryDiagramIsReferenced(t *testing.T) {
	root := repoRoot(t)
	svgs, _ := filepath.Glob(filepath.Join(root, "docs", "*.svg"))

	var allDocs string
	for _, body := range trackedDocs(t) {
		allDocs += body
	}
	gen, err := os.ReadFile(filepath.Join(root, "docs", "make-theme-variants.py"))
	if err != nil {
		t.Fatalf("the variant generator is missing: %v", err)
	}

	for _, path := range svgs {
		name := filepath.Base(path)
		stem := strings.TrimSuffix(name, ".svg")

		// Variants are embedded in the docs.
		if strings.HasSuffix(stem, "-light") || strings.HasSuffix(stem, "-dark") {
			if !strings.Contains(allDocs, name) {
				t.Errorf("%s is not embedded by any doc; either use it or delete it", name)
			}
			continue
		}
		// A base file is the source the variants are generated from, so it belongs
		// to the generator rather than to a doc.
		if !strings.Contains(string(gen), `"`+stem+`"`) {
			t.Errorf("%s is neither embedded nor listed in make-theme-variants.py", name)
		}
	}
}
