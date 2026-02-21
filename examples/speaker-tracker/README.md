# Speaker Tracker

A companion tool that reads wacli's SQLite database to track speakers across WhatsApp conversations.

## Features

- **Speaker Profiles**: Track who you've talked to, when you first/last saw them, and across how many chats
- **Privacy-Preserving**: JIDs are hashed (SHA256) for cross-chat tracking
- **Opt-Out Support**: Remove any contact from tracking with a single command
- **Conversation Notes**: Add notes about conversations for future reference
- **Non-Intrusive**: Reads from wacli.db (read-only), writes to a separate database

## Requirements

- Python 3.8+
- wacli running with `wacli sync`
- No additional dependencies (uses stdlib only)

## Installation

```bash
# Copy to your preferred location
cp speaker-tracker.py ~/.local/bin/
chmod +x ~/.local/bin/speaker-tracker.py
```

## Usage

```bash
# Process new messages (run periodically or via cron)
python3 speaker-tracker.py process

# List tracked speakers
python3 speaker-tracker.py speakers --limit 20

# Get details for a specific speaker
python3 speaker-tracker.py speaker +14155551234@s.whatsapp.net

# Opt out a contact (removes all data and prevents future tracking)
python3 speaker-tracker.py opt-out +14155551234@s.whatsapp.net

# List conversation notes
python3 speaker-tracker.py notes

# Add a note about a conversation
python3 speaker-tracker.py add-note --chat 120363xxx@g.us --summary "Discussed project timeline"
```

## Cron Setup

```bash
# Process new messages every 6 hours
0 */6 * * * /usr/bin/python3 ~/.local/bin/speaker-tracker.py process --json >> ~/.local/log/speaker-tracker.log 2>&1
```

## Database Schema

The tracker creates its own SQLite database (default: `~/.clawdbot/data/speaker-tracker.db`) with:

- `speaker_profiles`: Basic speaker info with hashed JIDs
- `speaker_interests`: Topic tracking (placeholder for NLP integration)
- `conversation_notes`: Manual notes about conversations
- `opted_out`: Privacy opt-out list

## Options

```
--wacli-db PATH     Path to wacli database (default: ~/.wacli/wacli.db)
--tracker-db PATH   Path to tracker database (default: ~/.clawdbot/data/speaker-tracker.db)
--json              Output as JSON
```

## Privacy Model

- JIDs are hashed using SHA256 before storage
- Display names are stored locally only
- The `opt-out` command:
  - Stores the raw JID in an opt-out table (to prevent re-tracking)
  - Deletes all existing profile and interest data for that contact
  - Prevents any future tracking

## Integration Ideas

This example demonstrates how to build on wacli's data. Other possibilities:

- **Interest extraction**: Add NLP to extract topics from messages
- **Relationship graphs**: Track who talks to whom in groups
- **Conversation summaries**: Use LLMs to summarize daily conversations
- **CRM integration**: Sync speaker profiles to HubSpot, Salesforce, etc.

## License

Same as wacli (MIT)
