# contacts

Read when: finding synced contacts, importing macOS Contacts names, or managing local contact metadata.

`wacli contacts` works with contact metadata stored locally. Aliases and tags are local to `wacli`; they do not edit WhatsApp contacts on the phone. `contacts save` is the exception: it saves the name to the WhatsApp account itself.

## Commands

```bash
wacli contacts search <query> [--limit N]
wacli contacts show --jid JID
wacli contacts resolve <lid|phone|jid> [...]
wacli contacts check <phone> [phone...]
wacli contacts save --account-wide --jid PHONE_OR_JID --name NAME [--first-name NAME] [--phonebook]
wacli contacts refresh
wacli contacts import-system [--input FILE] [--dry-run] [--clear]
wacli contacts alias set --jid JID --alias NAME
wacli contacts alias rm --jid JID
wacli contacts tags add --jid JID --tag TAG
wacli contacts tags rm --jid JID --tag TAG
```

## Notes

- `search` matches alias, full name, push name, first name, business name, phone, and JID.
- `search` and `show` combine phone-number and `@lid` contact rows only when the local WhatsApp session has a verified mapping. Matching names alone never merge contacts. The combined result uses the phone-number JID and its actual phone number; an unmapped `@lid` stays separate with an empty `phone` field.
- Search matches metadata and stored IDs on either row, as well as the resolved phone number. `--limit` applies after combining duplicates. A contact stored only as `@lid` is also searchable by its mapped phone number, including partial numbers.
- For combined contacts, local aliases and system names retain their display precedence, with the phone-number row winning conflicts within each field. `show` accepts either stored JID or the resolved phone-number JID and combines tags. Reading contacts never rewrites their stored rows or metadata.
- Alias and tag commands accept either verified identity, including the JID returned by `search` or `show`. Changes apply atomically to both identities so removing metadata cannot reveal an older copy on the other row. These local commands read the session mapping without connecting to WhatsApp or modifying its session database; an unreadable session database fails the command before a metadata write.
- `resolve` maps each LID to its phone number and each phone number to its LID, using the local session's verified mapping. It reads local state only and takes no store lock, so it works while `sync --follow` is running. Every input gets an entry: an identity without a known pair has `resolved: false` (never dropped), and a group, channel or malformed input carries an `error`. JSON entries have `input`, `jid` (phone JID), `phone`, `lid`, `name`, `resolved` and `error` (#420).
- `check` connects with the account session and asks WhatsApp's servers whether each number is registered (accepts +E164, common formatting, or user JIDs). Results are reported per query and not stored locally; use `--json` for scripting. A number the server did not answer for is reported as `no response` (JSON `"responded": false`) — treat it as unknown, not as a confirmed negative.
- While a same-store `sync --follow` process owns the store lock, `check` is delegated to its already-connected session instead of failing on the lock. Restart an older `sync --follow` process after upgrading; older daemons return an unsupported `contacts_check` kind error. Delegated checks run in the follow process's serialized delegate queue, so a large batch can hold a queued send until the send's own `--timeout` expires; split long lists or run them when nothing else is sending.
- `save --account-wide` explicitly opts into saving or renaming a contact in the WhatsApp account, like "Save contact" in the app. It sends the same app state `contact` mutation the official clients send, so the name syncs to the phone and linked devices and is what WhatsApp shows for that number. Running it again with another name renames the contact. `--first-name` defaults to the first word of `--name`. Names are trimmed and must not contain line breaks or control characters; this is checked before anything is opened or sent. Without `--account-wide`, the command refuses before connecting or delegating; `--phonebook` alone does not authorize the account edit. Local aliases, imports, refresh and ordinary sync never initiate account contact writes. The separate, default-off `--phonebook` flag asks the phone to also write the contact to the device address book. It connects with the account session and needs the store lock; while a same-store `sync --follow` process owns it, `save` is delegated to that process like the `chats` state commands (it skips the send queue and pacing but keeps the caller's `--timeout`). Older daemons return an unsupported `contacts_save` kind error; restart `sync --follow` after upgrading. `--read-only` is refused before any connection or delegation.
- `save` takes a phone number or phone-number JID (not a `@lid`; a JID must have a numeric user part) and resolves the contact's LID first, locally or over the connection, because current WhatsApp clients key contacts by LID and show only the number for a contact saved without it. When the LID cannot be found, nothing is saved. On success the local index gets the name from whatsmeow's echo of the save (see below). WhatsApp offers no deletion through this mutation; save another name to change it.
- `save` waits for other app-state writes and recovery, then catches up contact changes before sending. It mirrors the contact changes applied during the send before returning, including newer phone renames and other contacts' edits. It does not optimistically overwrite a newer name with the requested name.
- A save keeps durable recovery debt when it cannot confirm that its whole batch was applied and mirrored, and warns `contact_save_coverage_unknown`. An interrupted save can leave the same debt. The next sync or contact write repairs it using the usual app-state recovery: full replay first, then a phone snapshot if needed. Collections already known to fail full replay keep their existing degraded recovery behavior. If mirroring fails, the debt stays until a later successful mirror or recovery. `contact_save_echo_pending` means WhatsApp accepted the save but its name has not been fetched back yet; run `sync` to catch up.
- Contact edits received from the phone or another linked device are mirrored into `wacli.db`, so `show` and `search` update without `contacts refresh`. The applied order is preserved across phone-number and mapped LID entries. Names are authoritative: a rename without a first name clears the old first name, and an edit without names clears both. Push, business, and macOS Contacts names are kept.
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
wacli contacts resolve 123456789@lid 987654321@lid --json
wacli contacts check +43 664 12345678 --json
wacli contacts refresh
wacli contacts import-system --dry-run
wacli contacts save --account-wide --jid +1234567890 --name "Alice Smith"
wacli contacts alias set --jid 1234567890@s.whatsapp.net --alias mom
wacli contacts tags add --jid 1234567890@s.whatsapp.net --tag family
```
