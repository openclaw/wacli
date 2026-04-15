# Contributing to wacli

We love contributions! This project is designed to be agent-friendly. If you are using an AI agent (like Gemini CLI, Claude Code, or Cursor) to contribute, follow these guidelines to ensure high-quality code and context alignment.

## Tech Stack
- **Go 1.21+** (Generics, SQLite FTS5)
- **Cobra** for CLI
- **SQLite** for storage

## Agentic Workflow (GSD Protocol)
We recommend using the **GSD (Get Shit Done)** protocol for your contributions:
1. **Spec-First**: Create a plan before writing code.
2. **Atomic Commits**: Keep changes surgical and focused.
3. **Verification**: Always include verification steps in your plan.

## Adding New Features
- If adding a new CLI command, register it in `cmd/wacli/`.
- If modifying storage logic, update `internal/store/`.
- Use `internal/out/` for output formatting logic.

## Exporting to Obsidian (New Feature!)
You can now export messages directly to Obsidian-flavored Markdown:
```bash
wacli messages export --chat [JID] --output my_chat.md
```
This follows the Project Note Protocol for seamless integration with AI agents using Obsidian as a knowledge base.

## Submission
1. Fork the repo.
2. Create a feature branch.
3. Submit a PR with a clear description of the "Core Value" added.
