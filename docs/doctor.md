# doctor

Read when: diagnosing store layout, auth state, FTS/search support, locks, or optional live connectivity.

`wacli doctor` reports local health information and can optionally connect to WhatsApp.

## Command

```bash
wacli doctor [--connect]
```

## Notes

- Without `--connect`, doctor avoids live WhatsApp connection.
- `--connect` requires auth and the store lock.
- Output includes local store counts, auth identity when available, FTS/search state, and lock details.
- Use `--json` for machine-readable diagnostics.

## Examples

```bash
wacli doctor
wacli doctor --json
wacli doctor --connect
```
