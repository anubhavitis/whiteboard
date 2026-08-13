package agent

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Skill is one piece of guidance an agent can be given on top of the core canvas
// skill.
//
// Skills are markdown because they are content: a person should be able to read
// one in a diff, edit it in any editor, and understand what it does without
// reading Go.
type Skill struct {
	// ID is the filename without its extension, and is what the wire and the UI
	// refer to. Stable across edits to the body.
	ID string `json:"id"`
	// Name is the first markdown heading, or the id if there is none.
	Name string `json:"name"`
	// Description is the first paragraph after the heading — one line in the UI.
	Description string `json:"description"`
	// Body is the whole file, which is what actually reaches the model.
	Body string `json:"-"`
	// BuiltIn skills ship with the binary and cannot be edited or deleted.
	BuiltIn bool `json:"built_in"`
	// Tokens is a rough cost estimate, shown in the UI because every enabled
	// skill is resent on every turn and competes with the canvas for context.
	Tokens int `json:"tokens"`
}

//go:embed skills/*.md
var builtInSkills embed.FS

// SkillStore holds the skills available to this process: the ones embedded in the
// binary, plus whatever markdown the user has dropped in the skills directory.
//
// User skills live on disk and are deliberately NOT part of the repository — they
// are one person's notes about how they want their own agent to behave, not
// project source. The directory is gitignored.
type SkillStore struct {
	dir string

	mu     sync.RWMutex
	skills map[string]Skill
}

// NewSkillStore loads built-in skills, then any user skills from dir. A missing
// or unreadable directory is not an error: the user simply has none yet.
func NewSkillStore(dir string) (*SkillStore, error) {
	s := &SkillStore{dir: dir, skills: map[string]Skill{}}

	entries, err := builtInSkills.ReadDir("skills")
	if err != nil {
		return nil, fmt.Errorf("reading built-in skills: %w", err)
	}
	for _, e := range entries {
		raw, err := builtInSkills.ReadFile("skills/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("reading built-in skill %s: %w", e.Name(), err)
		}
		sk := parseSkill(idFromFilename(e.Name()), string(raw))
		sk.BuiltIn = true
		s.skills[sk.ID] = sk
	}

	if err := s.loadUserSkills(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadUserSkills reads dir, letting a user skill shadow a built-in of the same id
// so a shipped skill can be overridden locally without editing the binary.
func (s *SkillStore) loadUserSkills() error {
	if s.dir == "" {
		return nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("reading skills dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			// One unreadable file must not hide every other skill.
			continue
		}
		sk := parseSkill(idFromFilename(e.Name()), string(raw))
		s.mu.Lock()
		s.skills[sk.ID] = sk
		s.mu.Unlock()
	}
	return nil
}

// Reload re-reads the user directory, so editing a file in an editor takes effect
// without restarting the server.
func (s *SkillStore) Reload() error {
	return s.loadUserSkills()
}

// List returns every skill, built-ins first, then user skills, each alphabetical.
func (s *SkillStore) List() []Skill {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]Skill, 0, len(s.skills))
	for _, sk := range s.skills {
		out = append(out, sk)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].BuiltIn != out[j].BuiltIn {
			return out[i].BuiltIn
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get returns one skill by id.
func (s *SkillStore) Get(id string) (Skill, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sk, ok := s.skills[id]
	return sk, ok
}

// Compose builds the guidance for a turn: the core canvas skill, then each
// selected skill in the order the store lists them.
//
// The core skill is always included and is not selectable. Its rules are the ones
// the code enforces — no raw coordinates, ids are not names, never claim an edit
// that did not happen — so an agent without it does not read as "fewer skills",
// it reads as broken.
func (s *SkillStore) Compose(selected []string) string {
	parts := []string{CanvasSkill}

	want := map[string]bool{}
	for _, id := range selected {
		want[id] = true
	}
	for _, sk := range s.List() {
		if want[sk.ID] {
			parts = append(parts, sk.Body)
		}
	}
	return strings.Join(parts, "\n\n")
}

// Save writes a user skill. Built-in ids are rejected: shadowing one by hand is
// deliberate, doing it accidentally through the UI is not.
func (s *SkillStore) Save(id, body string) error {
	if s.dir == "" {
		return fmt.Errorf("no skills directory configured")
	}
	if !validID.MatchString(id) {
		return fmt.Errorf("id must be lowercase letters, digits and dashes")
	}
	if existing, ok := s.Get(id); ok && existing.BuiltIn {
		return fmt.Errorf("%q is a built-in skill and cannot be replaced", id)
	}
	if strings.TrimSpace(body) == "" {
		return fmt.Errorf("a skill needs a body")
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(s.dir, id+".md")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		return err
	}
	sk := parseSkill(id, body)
	s.mu.Lock()
	s.skills[id] = sk
	s.mu.Unlock()
	return nil
}

// Delete removes a user skill. Built-ins are part of the binary and cannot go.
func (s *SkillStore) Delete(id string) error {
	sk, ok := s.Get(id)
	if !ok {
		return fmt.Errorf("no skill %q", id)
	}
	if sk.BuiltIn {
		return fmt.Errorf("%q is built in and cannot be deleted", id)
	}
	if err := os.Remove(filepath.Join(s.dir, id+".md")); err != nil && !os.IsNotExist(err) {
		return err
	}
	s.mu.Lock()
	delete(s.skills, id)
	s.mu.Unlock()
	return nil
}

// validID keeps ids usable as both filenames and wire values.
var validID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func idFromFilename(name string) string {
	return strings.TrimSuffix(name, filepath.Ext(name))
}

var headingLine = regexp.MustCompile(`(?m)^#\s+(.+)$`)

// parseSkill pulls a display name and one-line description out of the markdown,
// so the UI needs no separate metadata file to keep in sync with the body.
func parseSkill(id, raw string) Skill {
	body := strings.TrimSpace(raw)
	sk := Skill{ID: id, Name: id, Body: body, Tokens: estimateTokens(body)}

	if m := headingLine.FindStringSubmatch(body); m != nil {
		sk.Name = strings.TrimSpace(m[1])
	}

	// The description is the first non-heading, non-empty line.
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sk.Description = firstSentence(line)
		break
	}
	return sk
}

// firstSentence trims a paragraph to something that fits one line of UI.
func firstSentence(s string) string {
	s = strings.NewReplacer("**", "", "*", "", "`", "").Replace(s)
	if i := strings.Index(s, ". "); i > 0 && i < 160 {
		return s[:i+1]
	}
	if len(s) > 160 {
		return strings.TrimSpace(s[:157]) + "…"
	}
	return s
}

// estimateTokens is the same ~4-chars-per-token rule the frontend uses for the
// canvas budget. Rough on purpose: it exists so the UI can show that skills are
// not free, not to be exact.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}
