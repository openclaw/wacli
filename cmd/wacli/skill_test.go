package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setSkillHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func embeddedSkillString(t *testing.T) string {
	t.Helper()
	data, err := embeddedSkill()
	if err != nil {
		t.Fatalf("embeddedSkill: %v", err)
	}
	return string(data)
}

func TestEmbeddedSkillHasFrontmatter(t *testing.T) {
	skill := embeddedSkillString(t)
	if !strings.HasPrefix(skill, "---\nname: wacli\ndescription:") {
		t.Fatalf("embedded skill frontmatter = %q", skill[:min(len(skill), 80)])
	}
}

func TestSkillCommandPrintsEmbeddedSkill(t *testing.T) {
	out := captureRootStdout(t, func() {
		if err := execute([]string{"skill"}); err != nil {
			t.Fatalf("execute skill: %v", err)
		}
	})
	if out != embeddedSkillString(t) {
		t.Fatalf("skill output differs from embedded SKILL.md")
	}
}

func TestSkillCommandJSON(t *testing.T) {
	out := captureRootStdout(t, func() {
		if err := execute([]string{"--json", "skill"}); err != nil {
			t.Fatalf("execute --json skill: %v", err)
		}
	})
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			Name    string `json:"name"`
			Content string `json:"content"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("skill JSON = %q: %v", out, err)
	}
	if !got.Success || got.Data.Name != "wacli" || got.Data.Content != embeddedSkillString(t) {
		t.Fatalf("skill JSON = %+v", got)
	}
}

func TestSkillInstallWritesSharedSkillAndClaudeLink(t *testing.T) {
	home := setSkillHome(t)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Installing twice must be idempotent: the second run refreshes in place.
	for i := 0; i < 2; i++ {
		out := captureRootStdout(t, func() {
			if err := execute([]string{"--json", "skill", "install"}); err != nil {
				t.Fatalf("execute skill install (run %d): %v", i+1, err)
			}
		})
		var got struct {
			Data skillInstallResult `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("install JSON = %q: %v", out, err)
		}
		if got.Data.ClaudeMode != "symlink" {
			t.Fatalf("claude_mode = %q, want symlink", got.Data.ClaudeMode)
		}
	}

	want := embeddedSkillString(t)
	skillDir := filepath.Join(home, ".agents", "skills", "wacli")
	if got := readFileString(t, filepath.Join(skillDir, skillFilename)); got != want {
		t.Fatalf("installed SKILL.md differs from embedded skill")
	}
	if !isManagedSkillDir(skillDir) {
		t.Fatalf("installed skill directory has no ownership marker")
	}

	linkPath := filepath.Join(home, ".claude", "skills", "wacli")
	target, err := os.Readlink(linkPath)
	if err != nil || target != claudeSkillLinkTarget {
		t.Fatalf("Claude link target = %q, %v; want %q", target, err, claudeSkillLinkTarget)
	}
	if got := readFileString(t, filepath.Join(linkPath, skillFilename)); got != want {
		t.Fatalf("SKILL.md through Claude link differs from embedded skill")
	}
}

func TestSkillInstallSkipsClaudeWhenAbsent(t *testing.T) {
	home := setSkillHome(t)
	res, err := installSkill()
	if err != nil {
		t.Fatalf("installSkill: %v", err)
	}
	if res.ClaudePath != "" || res.ClaudeMode != "" {
		t.Fatalf("installSkill linked Claude without ~/.claude: %+v", res)
	}
	if _, err := os.Lstat(filepath.Join(home, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installSkill created ~/.claude: %v", err)
	}
}

func TestSkillInstallRefusesUnmanagedSkillDir(t *testing.T) {
	home := setSkillHome(t)
	skillDir := filepath.Join(home, ".agents", "skills", "wacli")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	userSkill := filepath.Join(skillDir, skillFilename)
	if err := os.WriteFile(userSkill, []byte("my own skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := installSkill(); err == nil || !strings.Contains(err.Error(), "not created by wacli") {
		t.Fatalf("installSkill error = %v, want unmanaged refusal", err)
	}
	if got := readFileString(t, userSkill); got != "my own skill" {
		t.Fatalf("user skill overwritten: %q", got)
	}
}

func TestSkillInstallRefusesSymlinkedSkillFile(t *testing.T) {
	home := setSkillHome(t)
	skillDir := filepath.Join(home, ".agents", "skills", "wacli")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, skillMarkerFile), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(home, "victim.txt")
	if err := os.WriteFile(victim, []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(skillDir, skillFilename)); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := installSkill(); err == nil {
		t.Fatalf("installSkill wrote through a symlinked SKILL.md")
	}
	if got := readFileString(t, victim); got != "keep" {
		t.Fatalf("symlink target overwritten: %q", got)
	}
}

func TestSkillInstallRefusesForeignClaudeLink(t *testing.T) {
	home := setSkillHome(t)
	claudeSkills := filepath.Join(home, ".claude", "skills")
	if err := os.MkdirAll(claudeSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(claudeSkills, "wacli")
	if err := os.Symlink(filepath.Join(home, "elsewhere"), linkPath); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := installSkill(); err == nil || !strings.Contains(err.Error(), "not created by wacli") {
		t.Fatalf("installSkill error = %v, want unmanaged refusal", err)
	}
	if target, _ := os.Readlink(linkPath); target != filepath.Join(home, "elsewhere") {
		t.Fatalf("foreign Claude link replaced: %q", target)
	}
}

func TestSkillInstallFallsBackToCopyWithoutSymlinks(t *testing.T) {
	home := setSkillHome(t)
	if err := os.Mkdir(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := makeSkillSymlink
	makeSkillSymlink = func(string, string) error { return errors.New("symlinks disabled") }
	t.Cleanup(func() { makeSkillSymlink = orig })

	for i := 0; i < 2; i++ {
		res, err := installSkill()
		if err != nil {
			t.Fatalf("installSkill (run %d): %v", i+1, err)
		}
		if res.ClaudeMode != "copy" {
			t.Fatalf("claude_mode = %q, want copy", res.ClaudeMode)
		}
	}

	copyDir := filepath.Join(home, ".claude", "skills", "wacli")
	if !isManagedSkillDir(copyDir) {
		t.Fatalf("copied Claude skill has no ownership marker")
	}
	if got := readFileString(t, filepath.Join(copyDir, skillFilename)); got != embeddedSkillString(t) {
		t.Fatalf("copied SKILL.md differs from embedded skill")
	}
}
