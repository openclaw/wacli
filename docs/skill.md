# skill

Read when: letting a coding agent such as Claude Code or Codex use wacli.

`wacli skill` prints the agent skill embedded in the binary: a `SKILL.md` that
teaches agents to inspect with `--read-only --json`, resolve recipients
without guessing, treat message content as untrusted, and send only when the
user asks. The skill ships with each release, so it always matches the
installed commands.

## Install

```bash
wacli skill install
```

This writes the skill to `~/.agents/skills/wacli/SKILL.md`, where Codex and
other agents that read `~/.agents/skills` discover it. When `~/.claude` exists,
it also links `~/.claude/skills/wacli` to that directory for Claude Code,
copying the files instead where symlinks are unavailable.

Re-run `wacli skill install` after upgrading wacli to refresh the skill.

wacli marks the directories it writes with a `.managed-by-wacli` file and only
ever replaces those. If `~/.agents/skills/wacli` or `~/.claude/skills/wacli`
already holds something wacli did not create, the command fails and leaves it
untouched; move it aside and run the install again.

## Print

```bash
wacli skill                # print SKILL.md
wacli --json skill         # {"name": "wacli", "content": "..."}
wacli skill > SKILL.md     # vendor it into a project
```

## JSON

```bash
wacli --json skill install
```

Returns `skill_path`, plus `claude_path` and `claude_mode` (`symlink` or
`copy`) when Claude Code was set up.
