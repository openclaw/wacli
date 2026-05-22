# groups

Read when: listing, refreshing, inspecting, renaming, joining, leaving, inviting, pruning stale local group rows, or managing group participants.

`wacli groups` combines local group rows with live WhatsApp operations. Commands that mutate WhatsApp require writable mode.

## Commands

```bash
wacli groups list [--query TEXT] [--limit N]
wacli groups refresh
wacli groups create --name NAME [--user PHONE_OR_JID ...] [--announce-only] [--locked] [--join-approval] [--community|--linked-parent GROUP_JID]
wacli groups info --jid GROUP_JID
wacli groups rename --jid GROUP_JID --name NAME
wacli groups topic --jid GROUP_JID --text TEXT
wacli groups description --jid GROUP_JID --text TEXT
wacli groups announce-only --jid GROUP_JID (--on|--off)
wacli groups locked --jid GROUP_JID (--on|--off)
wacli groups leave --jid GROUP_JID
wacli groups participants add --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants remove --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants promote --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants demote --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups requests list --jid GROUP_JID
wacli groups requests approve --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups requests reject --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups invite link get --jid GROUP_JID
wacli groups invite link revoke --jid GROUP_JID
wacli groups join --code INVITE_CODE
wacli groups prune [--days N] [--left-only=false|--include-active] [--dry-run] [--confirm]
```

## Notes

- Group JIDs use the `...@g.us` server.
- `list` reads local rows and hides groups marked left. Human output includes the group type (`group`, `community`, or `subgroup`) and parent community JID when known.
- `list --json` includes `IsParent` for communities and `LinkedParentJID` for subgroups.
- `refresh` fetches joined groups live and updates local rows, including WhatsApp Community hierarchy metadata exposed by whatsmeow.
- `info` fetches one group live and persists it, including whether the chat is a Community parent or linked subgroup.
- `create` returns the new live group info and persists it locally. Use `--community` to create a community parent, or `--linked-parent` to create a subgroup inside an existing community.
- `topic` and `description` both set the WhatsApp group description. Passing `--text ""` clears it.
- `announce-only --on` makes the group admin-send-only; `--off` restores participant sends.
- `locked --on` makes group info editable only by admins; `--off` allows member edits again.
- `requests` lists, approves, or rejects pending join requests for groups with join approval enabled.
- `leave` marks the group left locally after WhatsApp confirms.
- `prune` only deletes local group/chat/message rows from `wacli.db`. It does not leave WhatsApp groups or delete anything from WhatsApp servers.
- `prune` defaults to groups marked left locally. `--days N` limits left-group pruning to groups left more than `N` days ago.
- `prune --include-active --days N` also targets active groups whose last known local message is older than `N` days. Groups with no known local activity timestamp are skipped.
- Use `prune --dry-run` before deleting and `--confirm` only after reviewing the target list.
- Participant users accept phone numbers with common formatting or JIDs.
- Invite `revoke` resets the invite link.

## Examples

```bash
wacli groups list --query family
wacli groups refresh
wacli groups create --name "Project launch" --user "+1 (234) 567-8900" --announce-only
wacli groups info --jid 123456789@g.us
wacli groups rename --jid 123456789@g.us --name "New name"
wacli groups topic --jid 123456789@g.us --text "Launch planning"
wacli groups announce-only --jid 123456789@g.us --on
wacli groups participants add --jid 123456789@g.us --user "+1 (234) 567-8900"
wacli groups requests approve --jid 123456789@g.us --user "+1 (234) 567-8900"
wacli groups invite link get --jid 123456789@g.us
wacli groups join --code AbCdEfGhIjK
wacli groups prune --dry-run
```
