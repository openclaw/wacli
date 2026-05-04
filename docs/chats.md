# chats

Read when: listing known chats or resolving one chat from the local store.

`wacli chats` reads chat rows from `wacli.db`. It can use session-backed PN/LID mappings to make historical `@lid` chat rows display as phone-number chats when possible.

## Commands

```bash
wacli chats list [--query TEXT] [--limit N]
wacli chats show --jid JID
```

## Notes

- `list` is local and sorted by newest known message timestamp.
- `--query` filters by chat name or JID.
- `show` accepts the stored JID. If a phone JID maps to a historical `@lid` row, it can show that row too.
- Run `wacli sync` or `wacli contacts refresh` to improve chat names.

## Examples

```bash
wacli chats list
wacli chats list --query family --limit 20
wacli chats show --jid 1234567890@s.whatsapp.net
```
