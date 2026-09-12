# doctor

Read when: diagnosing store layout, auth state, FTS/search support, locks, or optional live connectivity.

`wacli doctor` reports local health information and can optionally connect to WhatsApp.

## Command

```bash
wacli doctor [--connect]
```

## Notes

- Without `--connect`, doctor avoids live WhatsApp connection.
- `--connect` requires auth and the store lock, and waits for confirmed WhatsApp login before reporting `connected: true`. A socket handshake alone is not a successful login.
- Output includes local store counts, auth identity when available, FTS/search state, lock details, and `session_revoked`. An observed remote logout reports `authenticated: false` and `connection_state: "logged_out"` even when an old device row remains. Rejected or timed-out login attempts retain that state; confirmed login clears it.
- `--json` includes `store.last_activity_at` when a `HEARTBEAT` file is present, reflecting the last time `sync --follow` recorded observed activity. It is not a process-liveness marker; quiet healthy sessions may not update it because successful keepalives are silent. This is distinct from `store.last_sync_at`, which reflects the newest stored message timestamp.
- Use `--json` for machine-readable diagnostics.

## Examples

```bash
wacli doctor
wacli doctor --json
wacli doctor --connect
```
