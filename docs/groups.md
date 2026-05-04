# groups

Read when: listing, refreshing, inspecting, renaming, joining, leaving, inviting, or managing group participants.

`wacli groups` combines local group rows with live WhatsApp operations. Commands that mutate WhatsApp require writable mode.

## Commands

```bash
wacli groups list [--query TEXT] [--limit N]
wacli groups refresh
wacli groups info --jid GROUP_JID
wacli groups rename --jid GROUP_JID --name NAME
wacli groups leave --jid GROUP_JID
wacli groups participants add --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants remove --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants promote --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups participants demote --jid GROUP_JID --user PHONE_OR_JID [--user ...]
wacli groups invite link get --jid GROUP_JID
wacli groups invite link revoke --jid GROUP_JID
wacli groups join --code INVITE_CODE
```

## Notes

- Group JIDs use the `...@g.us` server.
- `list` reads local rows and hides groups marked left.
- `refresh` fetches joined groups live and updates local rows.
- `info` fetches one group live and persists it.
- `leave` marks the group left locally after WhatsApp confirms.
- Participant users accept phone numbers with common formatting or JIDs.
- Invite `revoke` resets the invite link.

## Examples

```bash
wacli groups list --query family
wacli groups refresh
wacli groups info --jid 123456789@g.us
wacli groups rename --jid 123456789@g.us --name "New name"
wacli groups participants add --jid 123456789@g.us --user "+1 (234) 567-8900"
wacli groups invite link get --jid 123456789@g.us
wacli groups join --code AbCdEfGhIjK
```
