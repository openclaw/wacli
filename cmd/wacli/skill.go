package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/openclaw/wacli/internal/out"
	"github.com/openclaw/wacli/skills"
	"github.com/spf13/cobra"
)

const (
	skillName       = "wacli"
	skillFilename   = "SKILL.md"
	skillMarkerFile = ".managed-by-wacli"
)

// claudeSkillLinkTarget is relative so the link survives a moved home
// directory. A link with any other target was not written by wacli.
var claudeSkillLinkTarget = filepath.Join("..", "..", ".agents", "skills", skillName)

// makeSkillSymlink is a seam so tests can exercise the copy fallback.
var makeSkillSymlink = os.Symlink

type skillInstallResult struct {
	SkillPath  string `json:"skill_path"`
	ClaudePath string `json:"claude_path,omitempty"`
	ClaudeMode string `json:"claude_mode,omitempty"`
}

func newSkillCmd(flags *rootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "Print or install the agent skill",
		Long:  "Print the SKILL.md embedded in this binary. It teaches coding agents to read with --read-only --json and to send only when asked.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := embeddedSkill()
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, map[string]string{"name": skillName, "content": string(data)})
			}
			_, err = os.Stdout.Write(data)
			return err
		},
	}
	cmd.AddCommand(newSkillInstallCmd(flags))
	return cmd
}

func newSkillInstallCmd(flags *rootFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the agent skill for coding agents",
		Long: "Write the embedded SKILL.md to ~/.agents/skills/wacli/ and, when ~/.claude exists, link it into ~/.claude/skills/wacli for Claude Code.\n\n" +
			"Re-run after upgrading wacli to refresh the skill. Paths that wacli did not create are never overwritten.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := installSkill()
			if err != nil {
				return err
			}
			if flags.asJSON {
				return out.WriteJSON(os.Stdout, res)
			}
			fmt.Fprintf(os.Stdout, "Installed skill to %s\n", res.SkillPath)
			switch res.ClaudeMode {
			case "symlink":
				fmt.Fprintf(os.Stdout, "Linked %s for Claude Code\n", res.ClaudePath)
			case "copy":
				fmt.Fprintf(os.Stdout, "Copied skill to %s for Claude Code (symlinks unavailable)\n", res.ClaudePath)
			}
			return nil
		},
	}
}

func embeddedSkill() ([]byte, error) {
	data, err := skills.FS.ReadFile(skillName + "/" + skillFilename)
	if err != nil {
		return nil, fmt.Errorf("read embedded skill: %w", err)
	}
	return data, nil
}

func installSkill() (skillInstallResult, error) {
	var res skillInstallResult
	home, err := os.UserHomeDir()
	if err != nil {
		return res, fmt.Errorf("resolve home directory: %w", err)
	}
	data, err := embeddedSkill()
	if err != nil {
		return res, err
	}

	skillDir := filepath.Join(home, ".agents", "skills", skillName)
	if err := writeManagedSkill(skillDir, data); err != nil {
		return res, err
	}
	res.SkillPath = filepath.Join(skillDir, skillFilename)

	claudeDir := filepath.Join(home, ".claude")
	if info, err := os.Stat(claudeDir); err != nil || !info.IsDir() {
		return res, nil
	}
	linkPath := filepath.Join(claudeDir, "skills", skillName)
	mode, err := linkClaudeSkill(linkPath, data)
	if err != nil {
		return res, err
	}
	res.ClaudePath = linkPath
	res.ClaudeMode = mode
	return res, nil
}

// writeManagedSkill writes SKILL.md into dir after claiming it with a marker
// file. A directory wacli did not create is left untouched.
func writeManagedSkill(dir string, data []byte) error {
	if err := claimSkillDir(dir); err != nil {
		return err
	}
	marker := []byte("Managed by wacli. `wacli skill install` overwrites this directory.\n")
	if err := writeSkillFile(filepath.Join(dir, skillMarkerFile), marker); err != nil {
		return err
	}
	return writeSkillFile(filepath.Join(dir, skillFilename), data)
}

// claimSkillDir accepts a missing, empty, or wacli-managed directory and
// refuses anything else, including a symlink whose target was never checked.
func claimSkillDir(dir string) error {
	info, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create skill directory: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("inspect skill directory: %w", err)
	case !info.IsDir():
		return unmanagedSkillPathError(dir)
	case isManagedSkillDir(dir):
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("inspect skill directory: %w", err)
	}
	if len(entries) > 0 {
		return unmanagedSkillPathError(dir)
	}
	return nil
}

func isManagedSkillDir(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, skillMarkerFile))
	return err == nil && info.Mode().IsRegular()
}

// writeSkillFile refuses to write through a symlink or other non-regular file
// planted at path, even inside a managed directory.
func writeSkillFile(path string, data []byte) error {
	info, err := os.Lstat(path)
	switch {
	case err == nil && !info.Mode().IsRegular():
		return unmanagedSkillPathError(path)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// linkClaudeSkill points ~/.claude/skills/wacli at the shared skill, falling
// back to a managed copy where symlinks are unavailable. It reports "symlink"
// or "copy".
func linkClaudeSkill(linkPath string, data []byte) (string, error) {
	info, err := os.Lstat(linkPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return "", fmt.Errorf("inspect %s: %w", linkPath, err)
	case info.Mode()&os.ModeSymlink != 0:
		if target, err := os.Readlink(linkPath); err == nil && target == claudeSkillLinkTarget {
			return "symlink", nil
		}
		return "", unmanagedSkillPathError(linkPath)
	case info.IsDir() && isManagedSkillDir(linkPath):
		// A copy left by an earlier fallback; refresh it in place.
		return "copy", writeManagedSkill(linkPath, data)
	default:
		return "", unmanagedSkillPathError(linkPath)
	}

	if err := os.MkdirAll(filepath.Dir(linkPath), 0o755); err != nil {
		return "", fmt.Errorf("create Claude skills directory: %w", err)
	}
	if err := makeSkillSymlink(claudeSkillLinkTarget, linkPath); err == nil {
		return "symlink", nil
	}
	if err := writeManagedSkill(linkPath, data); err != nil {
		return "", err
	}
	return "copy", nil
}

func unmanagedSkillPathError(path string) error {
	return fmt.Errorf("%s exists and was not created by wacli; move it aside and re-run `wacli skill install`", path)
}
