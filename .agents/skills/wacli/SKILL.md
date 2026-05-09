---
name: wacli
description: "wacli: WhatsApp linked devices, stores, sync/auth/send, repo/release work."
---

# Wacli

Use this for `wacli` repo work and local WhatsApp linked-device stores. Prefer read-only commands for inspection unless the user explicitly asks to auth, sync, send, mutate chats/groups, or release.

## Sources

- Repo: `~/Projects/wacli`
- CLI in repo: `./dist/wacli` after `pnpm build`
- Installed CLI: `wacli`
- Default config: `~/.wacli/config.yaml`
- Default macOS store: `~/.wacli`
- Named account stores: `~/.wacli/accounts/<name>`
- App DB: `<store>/wacli.db`
- WhatsApp session DB: `<store>/session.db`

## Safety

- Use `--read-only` or `WACLI_READONLY=1` for inspection.
- Use `--json` for parsing.
- Do not send messages unless explicitly asked.
- Do not write `session.db` directly.
- Do not merge account data into one `wacli.db`; named accounts are isolated stores.
- Watch dirty worktrees; leave unrelated files alone.

## Account Workflow

List accounts and store paths:

```bash
wacli accounts list --json
```

Inspect one account without connecting:

```bash
wacli --account me doctor --read-only --json
wacli --account me auth status --read-only --json
```

Use `--account NAME` for normal multi-account work. Use `--store DIR` only for one-off legacy/manual store debugging.

## Message/Store Checks

Prefer CLI first:

```bash
wacli --account me messages list --read-only --json --limit 20
wacli --account me messages search --read-only --json "query"
wacli --account me chats list --read-only --json
```

For DB health or aggregate checks, use SQLite read-only where possible:

```bash
sqlite3 "$HOME/.wacli/accounts/me/wacli.db" "pragma integrity_check;"
sqlite3 "$HOME/.wacli/accounts/me/wacli.db" \
  "select count(*) from messages;
   select count(*) from messages_fts;"
```

Useful consistency checks:

```sql
select count(*) from (
  select chat_jid, msg_id, count(*) c
  from messages
  group by chat_jid, msg_id
  having c > 1
);

select count(*)
from messages m
left join chats c on c.jid = m.chat_jid
where c.jid is null;

select count(*) from messages where revoked = 0 and deleted_for_me = 0;
select count(*) from messages_fts;
```

## Sync/Auth UX

`auth` pairs and then bootstraps sync. `sync` never shows QR and requires an authenticated store.

Common commands:

```bash
wacli --account me auth
wacli --account me sync --once
wacli --account me sync --follow
wacli --account me sync --once --events 2>events.ndjson
```

Interactive TTY sync progress should be concise; warnings must remain visible. `--events` must keep stderr as NDJSON.

## Repo Workflow

Read docs before coding when behavior changes:

```bash
pnpm -s docs:list || bin/docs-list || true
```

Focused tests first, then full gate:

```bash
go test ./internal/app
go test ./internal/store
pnpm docs:site && pnpm format:check && pnpm lint && pnpm test && pnpm build && git diff --check
```

User-facing changes need docs and `CHANGELOG.md`. Use `committer` with explicit file paths.

## Release

Read `docs/release.md` before release work. Release is tag-driven; verify workflow state with `gh run list/view`. If a release workflow is cancelled or partially failed, state exactly which jobs completed and which did not.
