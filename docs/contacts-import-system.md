# contacts import-system

Read when: importing macOS Contacts names into wacli, previewing matched phone numbers, clearing imported names, or feeding contacts from JSON/NDJSON.

`wacli contacts import-system` matches phone numbers from your system contacts against contacts already stored in `wacli.db`, then stores the system display name as local wacli metadata.

It does not modify WhatsApp, your phone contacts, or macOS Contacts.

## Before Importing

Run a contact refresh first so wacli has the latest WhatsApp-side contact rows:

```bash
wacli contacts refresh
```

Then preview the import:

```bash
wacli contacts import-system --dry-run
```

The dry run prints how many local contacts would receive a system name, plus skipped counts for contacts with no phone number, no system match, or an already-current system name.

## Apply

```bash
wacli contacts import-system
```

On macOS, this reads Contacts.app through the Contacts framework. macOS may prompt for Contacts permission the first time. If access is denied, grant Contacts access in System Settings and run the command again.

The command stores names in `contacts.system_name`. Display and search precedence is:

```text
alias > system_name > WhatsApp full/push/business/first name
```

Manual aliases still win. Use aliases for intentional local nicknames; use system names to mirror your address book display names.

## JSON

Use global `--json` for machine-readable output:

```bash
wacli --json contacts import-system --dry-run
```

The JSON response is wrapped in the standard envelope. Import details live under `.data`:

```json
{
  "success": true,
  "data": {
    "matched": 42,
    "matches": [
      {
        "jid": "1234567890@s.whatsapp.net",
        "phone": "1234567890",
        "current_name": "WhatsApp Name",
        "system_name": "Address Book Name"
      }
    ],
    "skipped_no_phone": 0,
    "skipped_no_match": 10,
    "skipped_same": 5,
    "dry_run": true
  },
  "error": null
}
```

## Import From A File

Use `--input FILE` to import from a JSON array or newline-delimited JSON instead of opening macOS Contacts:

Exports are limited to 10 MiB, whether read from a file or the macOS Contacts helper. An oversized helper is stopped before its full output is buffered; no contact names are imported from an oversized export.

```bash
wacli contacts import-system --input contacts.json --dry-run
wacli contacts import-system --input contacts.ndjson
```

Each contact object can contain `full_name`, `first_name`, `last_name`, and `phones`:

```json
[
  {
    "full_name": "Alice Appleseed",
    "phones": ["+1 (415) 734-7847"]
  }
]
```

NDJSON works too:

```json
{"full_name":"Alice Appleseed","phones":["+1 (415) 734-7847"]}
{"first_name":"Bob","last_name":"Builder","phones":["0043 664 104 2436"]}
```

Phone matching strips non-digits. Numbers with a leading international `00` prefix are normalized to the same digits as `+`.

## Clear Imported Names

Preview and clear imported system names:

```bash
wacli contacts import-system --clear --dry-run
wacli contacts import-system --clear
```

Clearing removes only `system_name` values. It does not remove contacts, aliases, tags, messages, WhatsApp data, or macOS Contacts entries.

## Verify

Show a contact and search by its imported system name:

```bash
wacli contacts show --jid 1234567890@s.whatsapp.net
wacli contacts search "Alice Appleseed"
```

`contacts show` includes `System Name:` when one is present. Search matches imported system names in addition to aliases, WhatsApp names, phone numbers, and JIDs.
