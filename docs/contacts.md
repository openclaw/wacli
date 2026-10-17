# contacts

Read when: finding synced contacts, importing macOS Contacts names, or managing local contact metadata.

`wacli contacts` works with contact metadata stored locally. Aliases and tags are local to `wacli`; they do not edit WhatsApp contacts on the phone. `contacts save` is the exception: it saves the name to the WhatsApp account itself.

## Commands

```bash
wacli contacts search <query> [--limit N]
wacli contacts show --jid JID
wacli contacts resolve <lid|phone|jid> [...]
wacli contacts check <phone> [phone...]
wacli contacts save --jid PHONE_OR_JID --name NAME [--first-name NAME] [--phonebook]
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
- `save` saves or renames a contact in the WhatsApp account, like "Save contact" in the app. It sends the same app state `contact` mutation the official clients send, so the name syncs to the phone and linked devices and is what WhatsApp shows for that number. Running it again with another name renames the contact. `--first-name` defaults to the first word of `--name`. Names are trimmed and must not contain line breaks or control characters; this is checked before anything is opened or sent. `--phonebook` asks the phone to also write the contact to the device address book. It connects with the account session and needs the store lock; while a same-store `sync --follow` process owns it, `save` is delegated to that process like the `chats` state commands (it skips the send queue and pacing but keeps the caller's `--timeout`). Older daemons return an unsupported `contacts_save` kind error; restart `sync --follow` after upgrading. `--read-only` is refused before any connection or delegation.
- `save` takes a phone number or phone-number JID (not a `@lid`; a JID must have a numeric user part) and resolves the contact's LID first, locally or over the connection, because current WhatsApp clients key contacts by LID and show only the number for a contact saved without it. When the LID cannot be found, nothing is saved. On success the name is also written to the local index. WhatsApp offers no deletion through this mutation; save another name to change it.
- `save` is an app-state write like the `chats` state commands: it waits for other app-state writes and recovery, catches up the `critical_unblock_low` collection first, and records a durable recovery intent before sending. whatsmeow writes every contact mutation of the batches it applies for the send (our echo, a newer rename made on the phone, other contacts' edits, the patches of a conflict retry) to its own contact store before the send returns and only hands the events over later; wacli journals those store writes in the order the SDK makes them and `save` mirrors the journal into `wacli.db` under the place it reserved in the local persistence order. The batch therefore reaches `wacli.db` even if a one-shot `save` exits before whatsmeow dispatches the events, and the intent is settled once every write of the send has been mirrored. One window remains: a `save` killed after whatsmeow stored the batch (cursor and names) and before it mirrored it. whatsmeow persists the cursor before the names, so this is not atomic in the SDK either. The journal dies with that process, so the next `sync` start and the next contact write keep that intent, warn, and request the phone's recovery snapshot without a full sync; the snapshot settles it when whatsmeow accepts it, which the pinned whatsmeow only does when the snapshot is newer than the collection version the session already holds.
- The `critical_unblock_low` collection is never repaired with a full sync while local state exists. The pinned whatsmeow deletes the saved collection version and hash state (not the contact store) before it decodes a full sync, and after a `save` the phone can rewrite contact entries this device never stored, which makes the local hash stop verifying; our client cannot send the removal WhatsApp Web sends for such entries with the pinned whatsmeow. A hash failure requests the phone's recovery snapshot instead and keeps catching up incrementally, and an ordinary missing key is retried until whatsmeow receives it. Other collections keep their usual recovery.
- `contact` mutations received over the connection (the echo of a `save`, or edits made on the phone or another linked device while `sync --follow` runs or a `save` is connected) are mirrored into `wacli.db`, so `show` and `search` match the session store without a `contacts refresh`. The names come from the journal of whatsmeow's contact store writes, not from the event: whatsmeow writes its store synchronously while it applies a batch and dispatches each batch's events from its own goroutine, so a late or duplicated event cannot put an old name back, and the writes keep their order even when the phone indexes a contact by its LID while `save` indexes it by the phone number (whatsmeow keeps those as two independent rows). A mutation is authoritative for the full and first name, as in whatsmeow's own contact store: a rename without a first name clears the old one, and a mutation without names clears both, while push, business, and macOS Contacts names are kept. A mutation indexed by a LID the session can map is stored under the phone number; otherwise under that LID. A mutation whose session store write failed (for example on a cancelled context) is mirrored anyway, because whatsmeow decoded it and advanced the collection.
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
wacli contacts save --jid +1234567890 --name "Alice Smith"
wacli contacts alias set --jid 1234567890@s.whatsapp.net --alias mom
wacli contacts tags add --jid 1234567890@s.whatsapp.net --tag family
```
