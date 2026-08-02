# contacts

Read when: finding synced contacts, importing macOS Contacts names, or managing local contact metadata.

`wacli contacts` works with contact metadata stored locally. Aliases and tags are local to `wacli`; they do not edit WhatsApp contacts on the phone.

## Commands

```bash
wacli contacts search <query> [--limit N]
wacli contacts show --jid JID
wacli contacts check <phone> [phone...]
wacli contacts refresh
wacli contacts import-system [--input FILE] [--dry-run] [--clear]
wacli contacts alias set --jid JID --alias NAME
wacli contacts alias rm --jid JID
wacli contacts tags add --jid JID --tag TAG
wacli contacts tags rm --jid JID --tag TAG
```

## Notes

- `search` matches alias, full name, push name, first name, business name, phone, and JID.
- `check` connects with the account session and asks WhatsApp's servers whether each number is registered (accepts +E164, common formatting, or user JIDs). Results are reported per query and not stored locally; use `--json` for scripting.
- `refresh` imports contacts from the whatsmeow session store into `wacli.db`.
- `import-system` imports display names from macOS Contacts by matching phone numbers against already-synced wacli contacts. Run `contacts refresh` first.
- `import-system --input FILE` reads a JSON array or newline-delimited JSON contacts file with `full_name` and `phones` fields instead of opening macOS Contacts.
- Imported system names are local wacli metadata. They do not edit WhatsApp contacts or macOS Contacts.
- Display precedence is local alias, imported system name, then WhatsApp names.
- Use `import-system --dry-run` before writing. Use `import-system --clear` to remove imported system names.
- See [contacts import-system](contacts-import-system.md) for the full import workflow, JSON shape, file format, and verification steps.
- Tags are local grouping metadata for scripts and future workflows.

## Examples

```bash
wacli contacts search Alice
wacli contacts show --jid 1234567890@s.whatsapp.net
wacli contacts check +43 664 12345678 --json
wacli contacts refresh
wacli contacts import-system --dry-run
wacli contacts alias set --jid 1234567890@s.whatsapp.net --alias mom
wacli contacts tags add --jid 1234567890@s.whatsapp.net --tag family
```
