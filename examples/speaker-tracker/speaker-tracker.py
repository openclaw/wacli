#!/usr/bin/env python3
"""
Speaker Tracker - Track speakers and their interests across WhatsApp conversations.

Uses wacli's existing SQLite database for message data, maintains a separate
tracking database for speaker profiles and conversation notes.

Privacy: JIDs are hashed (SHA256) for cross-chat tracking.
"""

import argparse
import hashlib
import json
import os
import sqlite3
import sys
from datetime import datetime, timedelta
from pathlib import Path
from typing import Optional

# Default paths
WACLI_DB = Path.home() / ".wacli" / "wacli.db"
TRACKER_DB = Path.home() / ".clawdbot" / "data" / "speaker-tracker.db"


def hash_jid(jid: str) -> str:
    """Hash a JID for privacy-preserving storage."""
    return hashlib.sha256(jid.strip().encode()).hexdigest()


def init_tracker_db(db_path: Path) -> sqlite3.Connection:
    """Initialize the speaker tracker database."""
    db_path.parent.mkdir(parents=True, exist_ok=True)
    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    conn.execute("PRAGMA journal_mode=WAL")
    conn.execute("PRAGMA foreign_keys=ON")
    
    conn.executescript("""
        CREATE TABLE IF NOT EXISTS speaker_profiles (
            jid_hash TEXT PRIMARY KEY,
            display_name TEXT,
            first_seen INTEGER NOT NULL,
            last_seen INTEGER NOT NULL,
            chats_seen_in INTEGER DEFAULT 1,
            message_count INTEGER DEFAULT 1,
            updated_at INTEGER NOT NULL
        );
        
        CREATE INDEX IF NOT EXISTS idx_speaker_profiles_last_seen 
            ON speaker_profiles(last_seen);
        
        CREATE TABLE IF NOT EXISTS speaker_interests (
            jid_hash TEXT NOT NULL,
            topic TEXT NOT NULL,
            confidence REAL DEFAULT 0.5,
            last_mentioned INTEGER NOT NULL,
            mention_count INTEGER DEFAULT 1,
            updated_at INTEGER NOT NULL,
            PRIMARY KEY (jid_hash, topic),
            FOREIGN KEY (jid_hash) REFERENCES speaker_profiles(jid_hash) ON DELETE CASCADE
        );
        
        CREATE INDEX IF NOT EXISTS idx_speaker_interests_topic 
            ON speaker_interests(topic);
        
        CREATE TABLE IF NOT EXISTS conversation_notes (
            id INTEGER PRIMARY KEY AUTOINCREMENT,
            chat_jid TEXT NOT NULL,
            chat_name TEXT,
            date INTEGER NOT NULL,
            summary TEXT,
            participants TEXT,  -- comma-separated jid_hashes
            key_topics TEXT,    -- comma-separated topics
            message_count INTEGER DEFAULT 0,
            created_at INTEGER NOT NULL
        );
        
        CREATE INDEX IF NOT EXISTS idx_conversation_notes_chat 
            ON conversation_notes(chat_jid);
        CREATE INDEX IF NOT EXISTS idx_conversation_notes_date 
            ON conversation_notes(date);
        
        CREATE TABLE IF NOT EXISTS tracker_meta (
            key TEXT PRIMARY KEY,
            value TEXT,
            updated_at INTEGER NOT NULL
        );
        
        CREATE TABLE IF NOT EXISTS opted_out (
            jid TEXT PRIMARY KEY,
            opted_out_at INTEGER NOT NULL
        );
    """)
    conn.commit()
    return conn


def get_wacli_connection(db_path: Path) -> sqlite3.Connection:
    """Get a read-only connection to wacli database."""
    if not db_path.exists():
        raise FileNotFoundError(f"wacli database not found: {db_path}")
    conn = sqlite3.connect(f"file:{db_path}?mode=ro", uri=True)
    conn.row_factory = sqlite3.Row
    return conn


def get_last_processed_ts(conn: sqlite3.Connection) -> Optional[int]:
    """Get the last processed timestamp."""
    row = conn.execute(
        "SELECT value FROM tracker_meta WHERE key = 'last_processed_ts'"
    ).fetchone()
    return int(row['value']) if row else None


def set_last_processed_ts(conn: sqlite3.Connection, ts: int):
    """Set the last processed timestamp."""
    now = int(datetime.utcnow().timestamp())
    conn.execute("""
        INSERT INTO tracker_meta(key, value, updated_at)
        VALUES ('last_processed_ts', ?, ?)
        ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=excluded.updated_at
    """, (str(ts), now))
    conn.commit()


def is_opted_out(conn: sqlite3.Connection, jid: str) -> bool:
    """Check if a JID is opted out."""
    row = conn.execute("SELECT 1 FROM opted_out WHERE jid = ?", (jid,)).fetchone()
    return row is not None


def opt_out(conn: sqlite3.Connection, jid: str):
    """Opt out a JID from tracking."""
    now = int(datetime.utcnow().timestamp())
    jid_hash = hash_jid(jid)
    conn.execute(
        "INSERT OR REPLACE INTO opted_out(jid, opted_out_at) VALUES (?, ?)",
        (jid, now)
    )
    conn.execute("DELETE FROM speaker_interests WHERE jid_hash = ?", (jid_hash,))
    conn.execute("DELETE FROM speaker_profiles WHERE jid_hash = ?", (jid_hash,))
    conn.commit()


def upsert_speaker(conn: sqlite3.Connection, jid: str, display_name: str, seen_at: int):
    """Update or insert a speaker profile."""
    jid_hash = hash_jid(jid)
    now = int(datetime.utcnow().timestamp())
    
    conn.execute("""
        INSERT INTO speaker_profiles(jid_hash, display_name, first_seen, last_seen, 
                                     chats_seen_in, message_count, updated_at)
        VALUES (?, ?, ?, ?, 1, 1, ?)
        ON CONFLICT(jid_hash) DO UPDATE SET
            display_name=CASE WHEN excluded.display_name != '' 
                              THEN excluded.display_name 
                              ELSE speaker_profiles.display_name END,
            last_seen=CASE WHEN excluded.last_seen > speaker_profiles.last_seen 
                           THEN excluded.last_seen 
                           ELSE speaker_profiles.last_seen END,
            message_count=speaker_profiles.message_count + 1,
            updated_at=excluded.updated_at
    """, (jid_hash, display_name, seen_at, seen_at, now))


def increment_chat_count(conn: sqlite3.Connection, jid: str):
    """Increment the chats_seen_in count for a speaker."""
    jid_hash = hash_jid(jid)
    now = int(datetime.utcnow().timestamp())
    conn.execute("""
        UPDATE speaker_profiles
        SET chats_seen_in = chats_seen_in + 1, updated_at = ?
        WHERE jid_hash = ?
    """, (now, jid_hash))


def process_messages(tracker_conn: sqlite3.Connection, wacli_conn: sqlite3.Connection,
                     since: Optional[int] = None, chat_jid: Optional[str] = None,
                     limit: int = 10000, dry_run: bool = False) -> dict:
    """Process messages from wacli database to update speaker profiles."""
    
    # Determine start time
    if since is None:
        since = get_last_processed_ts(tracker_conn)
    if since is None:
        # Default to 7 days ago for first run
        since = int((datetime.utcnow() - timedelta(days=7)).timestamp())
    
    # Build query
    query = """
        SELECT m.chat_jid, COALESCE(c.name, '') as chat_name, m.msg_id, 
               COALESCE(m.sender_jid, '') as sender_jid, 
               COALESCE(m.sender_name, '') as sender_name,
               m.ts, m.from_me, COALESCE(m.text, '') as text
        FROM messages m
        LEFT JOIN chats c ON c.jid = m.chat_jid
        WHERE m.ts > ?
    """
    params = [since]
    
    if chat_jid:
        query += " AND m.chat_jid = ?"
        params.append(chat_jid)
    
    query += " ORDER BY m.ts ASC LIMIT ?"
    params.append(limit)
    
    messages = wacli_conn.execute(query, params).fetchall()
    
    if dry_run:
        return {
            "dry_run": True,
            "messages_found": len(messages),
            "since": since,
            "since_date": datetime.utcfromtimestamp(since).isoformat()
        }
    
    if not messages:
        return {
            "processed": 0,
            "since": since,
            "since_date": datetime.utcfromtimestamp(since).isoformat()
        }
    
    # Process messages
    speakers_seen = set()
    chat_speakers = {}  # chat -> set of jid_hashes
    latest_ts = since
    
    for msg in messages:
        sender_jid = msg['sender_jid']
        if not sender_jid or msg['from_me']:
            continue  # Skip messages without sender or from self
        
        # Check opt-out
        if is_opted_out(tracker_conn, sender_jid):
            continue
        
        # Update speaker profile
        display_name = msg['sender_name'] or sender_jid
        upsert_speaker(tracker_conn, sender_jid, display_name, msg['ts'])
        
        # Track chat participation
        chat_jid = msg['chat_jid']
        if chat_jid not in chat_speakers:
            chat_speakers[chat_jid] = set()
        
        jid_hash = hash_jid(sender_jid)
        if jid_hash not in chat_speakers[chat_jid]:
            chat_speakers[chat_jid].add(jid_hash)
            if jid_hash in speakers_seen:
                # Seen in another chat, increment
                increment_chat_count(tracker_conn, sender_jid)
        speakers_seen.add(jid_hash)
        
        if msg['ts'] > latest_ts:
            latest_ts = msg['ts']
    
    # Update last processed timestamp
    if latest_ts > since:
        set_last_processed_ts(tracker_conn, latest_ts)
    
    tracker_conn.commit()
    
    return {
        "processed": len(messages),
        "speakers_seen": len(speakers_seen),
        "chats_scanned": len(chat_speakers),
        "since": since,
        "since_date": datetime.utcfromtimestamp(since).isoformat(),
        "latest": latest_ts,
        "latest_date": datetime.utcfromtimestamp(latest_ts).isoformat()
    }


def list_speakers(conn: sqlite3.Connection, limit: int = 50) -> list:
    """List all tracked speakers."""
    rows = conn.execute("""
        SELECT jid_hash, display_name, first_seen, last_seen, 
               chats_seen_in, message_count, updated_at
        FROM speaker_profiles
        ORDER BY last_seen DESC
        LIMIT ?
    """, (limit,)).fetchall()
    
    return [dict(row) for row in rows]


def get_speaker(conn: sqlite3.Connection, jid: str) -> Optional[dict]:
    """Get a speaker profile by JID."""
    jid_hash = hash_jid(jid)
    row = conn.execute("""
        SELECT jid_hash, display_name, first_seen, last_seen,
               chats_seen_in, message_count, updated_at
        FROM speaker_profiles
        WHERE jid_hash = ?
    """, (jid_hash,)).fetchone()
    
    if not row:
        return None
    
    speaker = dict(row)
    
    # Get interests
    interests = conn.execute("""
        SELECT topic, confidence, last_mentioned, mention_count
        FROM speaker_interests
        WHERE jid_hash = ?
        ORDER BY confidence DESC, mention_count DESC
        LIMIT 10
    """, (jid_hash,)).fetchall()
    
    speaker['interests'] = [dict(i) for i in interests]
    return speaker


def add_note(conn: sqlite3.Connection, chat_jid: str, summary: str,
             topics: Optional[list] = None, date: Optional[int] = None) -> int:
    """Add a conversation note."""
    now = int(datetime.utcnow().timestamp())
    if date is None:
        date = now
    
    cursor = conn.execute("""
        INSERT INTO conversation_notes(chat_jid, chat_name, date, summary, 
                                       key_topics, message_count, created_at)
        VALUES (?, '', ?, ?, ?, 0, ?)
    """, (chat_jid, date, summary, ','.join(topics or []), now))
    
    conn.commit()
    return cursor.lastrowid


def list_notes(conn: sqlite3.Connection, chat_jid: Optional[str] = None, 
               limit: int = 50) -> list:
    """List conversation notes."""
    if chat_jid:
        rows = conn.execute("""
            SELECT id, chat_jid, chat_name, date, summary, key_topics, 
                   message_count, created_at
            FROM conversation_notes
            WHERE chat_jid = ?
            ORDER BY date DESC
            LIMIT ?
        """, (chat_jid, limit)).fetchall()
    else:
        rows = conn.execute("""
            SELECT id, chat_jid, chat_name, date, summary, key_topics,
                   message_count, created_at
            FROM conversation_notes
            ORDER BY date DESC
            LIMIT ?
        """, (limit,)).fetchall()
    
    return [dict(row) for row in rows]


def main():
    parser = argparse.ArgumentParser(description="Speaker Tracker for WhatsApp")
    parser.add_argument("--wacli-db", type=Path, default=WACLI_DB,
                        help="Path to wacli database")
    parser.add_argument("--tracker-db", type=Path, default=TRACKER_DB,
                        help="Path to tracker database")
    parser.add_argument("--json", action="store_true", help="Output as JSON")
    
    subparsers = parser.add_subparsers(dest="command", required=True)
    
    # process command
    process_parser = subparsers.add_parser("process", 
                                           help="Process messages to update profiles")
    process_parser.add_argument("--since", type=str, 
                                help="Process since date (YYYY-MM-DD)")
    process_parser.add_argument("--chat", type=str, help="Limit to specific chat")
    process_parser.add_argument("--limit", type=int, default=10000,
                                help="Max messages to process")
    process_parser.add_argument("--dry-run", action="store_true",
                                help="Show what would be processed")
    
    # speakers command
    speakers_parser = subparsers.add_parser("speakers", help="List tracked speakers")
    speakers_parser.add_argument("--limit", type=int, default=50)
    
    # speaker command
    speaker_parser = subparsers.add_parser("speaker", help="Show speaker details")
    speaker_parser.add_argument("jid", help="Speaker JID")
    
    # opt-out command
    optout_parser = subparsers.add_parser("opt-out", help="Opt out a contact")
    optout_parser.add_argument("jid", help="JID to opt out")
    
    # notes command
    notes_parser = subparsers.add_parser("notes", help="List conversation notes")
    notes_parser.add_argument("--chat", type=str, help="Filter by chat")
    notes_parser.add_argument("--limit", type=int, default=50)
    
    # add-note command
    addnote_parser = subparsers.add_parser("add-note", help="Add a conversation note")
    addnote_parser.add_argument("--chat", required=True, help="Chat JID")
    addnote_parser.add_argument("--summary", required=True, help="Note summary")
    addnote_parser.add_argument("--topics", type=str, help="Comma-separated topics")
    
    args = parser.parse_args()
    
    # Initialize databases
    tracker_conn = init_tracker_db(args.tracker_db)
    
    def output(data):
        if args.json:
            print(json.dumps(data, indent=2, default=str))
        else:
            if isinstance(data, list):
                for item in data:
                    print(item)
            elif isinstance(data, dict):
                for k, v in data.items():
                    print(f"{k}: {v}")
            else:
                print(data)
    
    if args.command == "process":
        wacli_conn = get_wacli_connection(args.wacli_db)
        since = None
        if args.since:
            since = int(datetime.strptime(args.since, "%Y-%m-%d").timestamp())
        result = process_messages(tracker_conn, wacli_conn, 
                                  since=since, chat_jid=args.chat,
                                  limit=args.limit, dry_run=args.dry_run)
        output(result)
        wacli_conn.close()
    
    elif args.command == "speakers":
        speakers = list_speakers(tracker_conn, args.limit)
        if args.json:
            output(speakers)
        else:
            print(f"{'NAME':<25} {'MSGS':>8} {'CHATS':>6} {'LAST SEEN':<12}")
            print("-" * 55)
            for sp in speakers:
                last_seen = datetime.utcfromtimestamp(sp['last_seen']).strftime('%Y-%m-%d')
                print(f"{sp['display_name'][:24]:<25} {sp['message_count']:>8} "
                      f"{sp['chats_seen_in']:>6} {last_seen:<12}")
    
    elif args.command == "speaker":
        speaker = get_speaker(tracker_conn, args.jid)
        if not speaker:
            print(f"Speaker not found: {args.jid}", file=sys.stderr)
            sys.exit(1)
        output(speaker)
    
    elif args.command == "opt-out":
        opt_out(tracker_conn, args.jid)
        output({"jid": args.jid, "opted_out": True})
    
    elif args.command == "notes":
        notes = list_notes(tracker_conn, args.chat, args.limit)
        output(notes)
    
    elif args.command == "add-note":
        topics = args.topics.split(",") if args.topics else None
        note_id = add_note(tracker_conn, args.chat, args.summary, topics)
        output({"id": note_id, "chat": args.chat})
    
    tracker_conn.close()


if __name__ == "__main__":
    main()
