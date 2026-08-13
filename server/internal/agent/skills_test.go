package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) (*SkillStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewSkillStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func TestBuiltInSkillsLoad(t *testing.T) {
	s, _ := newStore(t)
	list := s.List()
	if len(list) == 0 {
		t.Fatal("no built-in skills loaded; the go:embed pattern may be wrong")
	}
	for _, sk := range list {
		if !sk.BuiltIn {
			t.Errorf("%s should be built in", sk.ID)
		}
		if sk.Name == sk.ID {
			t.Errorf("%s has no markdown heading, so the UI would show a filename", sk.ID)
		}
		if sk.Description == "" {
			t.Errorf("%s has no description", sk.ID)
		}
		if sk.Tokens == 0 {
			t.Errorf("%s reports zero tokens", sk.ID)
		}
	}
}

// The core canvas skill must never appear as a selectable skill: disabling it
// would produce an agent that emits raw pixels and quotes ids, which reads as a
// regression rather than a choice.
func TestCoreSkillIsNotSelectable(t *testing.T) {
	s, _ := newStore(t)
	for _, sk := range s.List() {
		if sk.ID == "canvas_skill" || strings.Contains(sk.Name, "Working on the whiteboard") {
			t.Errorf("the core canvas skill is listed as optional: %s", sk.ID)
		}
	}
}

// Compose always includes the core, whatever is selected.
func TestComposeAlwaysIncludesCore(t *testing.T) {
	s, _ := newStore(t)
	for _, selected := range [][]string{nil, {}, {"flowcharts"}, {"nope"}} {
		got := s.Compose(selected)
		if !strings.Contains(got, CanvasSkill) {
			t.Errorf("selected=%v: core skill missing", selected)
		}
	}
}

func TestComposeIncludesSelectedAndExcludesOthers(t *testing.T) {
	s, _ := newStore(t)
	all := s.List()
	if len(all) < 2 {
		t.Skip("need two built-ins for this test")
	}
	pick, other := all[0], all[1]

	got := s.Compose([]string{pick.ID})
	if !strings.Contains(got, pick.Body) {
		t.Errorf("selected skill %s is not in the composed prompt", pick.ID)
	}
	if strings.Contains(got, other.Body) {
		t.Errorf("unselected skill %s leaked into the prompt", other.ID)
	}
}

func TestUnknownSkillIsIgnored(t *testing.T) {
	s, _ := newStore(t)
	if got := s.Compose([]string{"does-not-exist"}); got != s.Compose(nil) {
		t.Error("an unknown id should be ignored, not alter the prompt")
	}
}

func TestSaveAndLoadUserSkill(t *testing.T) {
	s, dir := newStore(t)
	body := "# Terse mode\n\nAnswer in one sentence. Never use a list."
	if err := s.Save("terse-mode", body); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "terse-mode.md")); err != nil {
		t.Errorf("skill was not written to disk: %v", err)
	}

	sk, ok := s.Get("terse-mode")
	if !ok {
		t.Fatal("saved skill is not in the store")
	}
	if sk.BuiltIn {
		t.Error("a user skill must not be marked built in")
	}
	if sk.Name != "Terse mode" {
		t.Errorf("Name = %q, want the markdown heading", sk.Name)
	}
	if !strings.Contains(s.Compose([]string{"terse-mode"}), "one sentence") {
		t.Error("the saved skill does not reach the composed prompt")
	}

	// A fresh store over the same dir must find it, so edits survive a restart.
	again, err := NewSkillStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := again.Get("terse-mode"); !ok {
		t.Error("a user skill did not survive a reload")
	}
}

func TestSaveRejectsBadInput(t *testing.T) {
	s, _ := newStore(t)
	cases := map[string][2]string{
		"empty body":     {"ok-id", "   "},
		"uppercase id":   {"BadID", "# x\n\nbody"},
		"spaces in id":   {"bad id", "# x\n\nbody"},
		"path traversal": {"../escape", "# x\n\nbody"},
		"slash in id":    {"a/b", "# x\n\nbody"},
	}
	for name, c := range cases {
		if err := s.Save(c[0], c[1]); err == nil {
			t.Errorf("%s: expected an error for id=%q", name, c[0])
		}
	}
}

// A built-in cannot be replaced or removed through the API: it lives in the
// binary, so "saving" over it would silently do nothing on the next restart.
func TestBuiltInsAreProtected(t *testing.T) {
	s, _ := newStore(t)
	built := s.List()[0]

	if err := s.Save(built.ID, "# hijacked\n\nnope"); err == nil {
		t.Error("overwriting a built-in should fail")
	}
	if err := s.Delete(built.ID); err == nil {
		t.Error("deleting a built-in should fail")
	}
	if sk, _ := s.Get(built.ID); sk.Body != built.Body {
		t.Error("the built-in body changed")
	}
}

func TestDeleteUserSkill(t *testing.T) {
	s, dir := newStore(t)
	if err := s.Save("gone-soon", "# Gone soon\n\nbody"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("gone-soon"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Get("gone-soon"); ok {
		t.Error("deleted skill is still in the store")
	}
	if _, err := os.Stat(filepath.Join(dir, "gone-soon.md")); !os.IsNotExist(err) {
		t.Error("the file is still on disk")
	}
	if err := s.Delete("gone-soon"); err == nil {
		t.Error("deleting a missing skill should report it")
	}
}

// Editing a file by hand should take effect without a restart.
func TestReloadPicksUpEdits(t *testing.T) {
	s, dir := newStore(t)
	path := filepath.Join(dir, "handmade.md")
	if err := os.WriteFile(path, []byte("# Handmade\n\nFirst version."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if sk, ok := s.Get("handmade"); !ok || !strings.Contains(sk.Body, "First version") {
		t.Fatal("hand-written skill was not picked up")
	}

	if err := os.WriteFile(path, []byte("# Handmade\n\nSecond version."), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	if sk, _ := s.Get("handmade"); !strings.Contains(sk.Body, "Second version") {
		t.Error("an edit on disk did not reach the store")
	}
}

// A non-existent directory is the normal state for a new user, not an error.
func TestMissingDirectoryIsFine(t *testing.T) {
	s, err := NewSkillStore(filepath.Join(t.TempDir(), "not-created-yet"))
	if err != nil {
		t.Fatalf("a missing skills dir should not fail: %v", err)
	}
	if len(s.List()) == 0 {
		t.Error("built-ins should still load")
	}
}

func TestNonMarkdownFilesAreIgnored(t *testing.T) {
	s, dir := newStore(t)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a skill"), 0o644)
	os.WriteFile(filepath.Join(dir, ".DS_Store"), []byte("junk"), 0o644)
	if err := s.Reload(); err != nil {
		t.Fatal(err)
	}
	for _, sk := range s.List() {
		if sk.ID == "notes" || strings.Contains(sk.ID, "DS_Store") {
			t.Errorf("non-markdown file was loaded as a skill: %s", sk.ID)
		}
	}
}
