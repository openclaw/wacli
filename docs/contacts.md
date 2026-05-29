# contacts

Read when: finding synced contacts, importing macOS Contacts names, or managing local contact metadata.

`wacli contacts` works with contact metadata stored locally. Aliases and tags are local to `wacli`; they do not edit WhatsApp contacts on the phone.

## Commands

```bash
wacli contacts search <query> [--limit N]
wacli contacts show --jid JID
wacli contacts refresh
wacli contacts import-system [--input FILE] [--dry-run] [--clear]
wacli contacts alias set --jid JID --alias NAME
wacli contacts alias rm --jid JID
wacli contacts tags add --jid JID --tag TAG
wacli contacts tags rm --jid JID --tag TAG
wacli contacts get-picture --jid JID --output PATH [--type preview|image] [--existing-id ID]
```

## Notes

- `search` matches alias, full name, push name, first name, business name, phone, and JID.
- `refresh` imports contacts from the whatsmeow session store into `wacli.db`.
- `import-system` imports display names from macOS Contacts by matching phone numbers against already-synced wacli contacts. Run `contacts refresh` first.
- `import-system --input FILE` reads a JSON array or newline-delimited JSON contacts file with `full_name` and `phones` fields instead of opening macOS Contacts.
- Imported system names are local wacli metadata. They do not edit WhatsApp contacts or macOS Contacts.
- Display precedence is local alias, imported system name, then WhatsApp names.
- Use `import-system --dry-run` before writing. Use `import-system --clear` to remove imported system names.
- See [contacts import-system](contacts-import-system.md) for the full import workflow, JSON shape, file format, and verification steps.
- Tags are local grouping metadata for scripts and future workflows.
- `get-picture` fetches the live profile picture from WhatsApp's CDN (the macOS WhatsApp app stops caching the thumbnail when a chat is archived, so this is the easiest way to recover an avatar for an archived contact). Default `--type preview` returns the 96x96 thumbnail; `--type image` returns the full-size picture (up to 640px). Pass `--output -` to stream JPEG bytes to stdout. `--existing-id` skips the download when the picture ID matches the supplied value. Combine `--json` with a file `--output` to get metadata (`id`, `type`, `url`, `output`, `bytes`) alongside the saved file; `--json` plus `--output -` is rejected because both would write to stdout. Errors are mapped: `no profile picture available` (`ErrProfilePictureNotSet`), `not authorized to view this profile picture` (`ErrProfilePictureUnauthorized`).

## Examples

```bash
wacli contacts search Alice
wacli contacts show --jid 1234567890@s.whatsapp.net
wacli contacts refresh
wacli contacts import-system --dry-run
wacli contacts alias set --jid 1234567890@s.whatsapp.net --alias mom
wacli contacts tags add --jid 1234567890@s.whatsapp.net --tag family
wacli contacts get-picture --jid 1234567890@s.whatsapp.net --output /tmp/alice.jpg
wacli contacts get-picture --jid 1234567890@s.whatsapp.net --type image --output /tmp/alice-full.jpg --json
```
