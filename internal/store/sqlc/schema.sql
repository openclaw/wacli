CREATE TABLE chats (
    jid TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    name TEXT,
    last_message_ts INTEGER,
    archived INTEGER NOT NULL DEFAULT 0,
    pinned INTEGER NOT NULL DEFAULT 0,
    muted_until INTEGER NOT NULL DEFAULT 0,
    unread INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE contacts (
    jid TEXT PRIMARY KEY,
    phone TEXT,
    push_name TEXT,
    full_name TEXT,
    first_name TEXT,
    business_name TEXT,
    system_name TEXT,
    updated_at INTEGER NOT NULL
);

CREATE TABLE groups (
    jid TEXT PRIMARY KEY,
    name TEXT,
    owner_jid TEXT,
    created_ts INTEGER,
    is_parent INTEGER NOT NULL DEFAULT 0,
    linked_parent_jid TEXT,
    left_at INTEGER,
    updated_at INTEGER NOT NULL
);

CREATE TABLE group_participants (
    group_jid TEXT NOT NULL,
    user_jid TEXT NOT NULL,
    role TEXT,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (group_jid, user_jid),
    FOREIGN KEY (group_jid) REFERENCES groups(jid) ON DELETE CASCADE
);

CREATE TABLE contact_aliases (
    jid TEXT PRIMARY KEY,
    alias TEXT NOT NULL,
    notes TEXT,
    updated_at INTEGER NOT NULL
);

CREATE TABLE contact_tags (
    jid TEXT NOT NULL,
    tag TEXT NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (jid, tag)
);

CREATE TABLE messages (
    rowid INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_jid TEXT NOT NULL,
    chat_name TEXT,
    msg_id TEXT NOT NULL,
    sender_jid TEXT,
    sender_name TEXT,
    ts INTEGER NOT NULL,
    from_me INTEGER NOT NULL,
    text TEXT,
    display_text TEXT,
    is_forwarded INTEGER NOT NULL DEFAULT 0,
    forwarding_score INTEGER NOT NULL DEFAULT 0,
    reaction_to_id TEXT,
    reaction_emoji TEXT,
    media_type TEXT,
    media_caption TEXT,
    filename TEXT,
    mime_type TEXT,
    direct_path TEXT,
    media_key BLOB,
    file_sha256 BLOB,
    file_enc_sha256 BLOB,
    file_length INTEGER,
    local_path TEXT,
    downloaded_at INTEGER,
    revoked INTEGER NOT NULL DEFAULT 0,
    deleted_for_me INTEGER NOT NULL DEFAULT 0,
    edited INTEGER NOT NULL DEFAULT 0,
    edited_ts INTEGER NOT NULL DEFAULT 0,
    buttons TEXT,
    UNIQUE(chat_jid, msg_id),
    FOREIGN KEY (chat_jid) REFERENCES chats(jid) ON DELETE CASCADE
);

CREATE TABLE starred (
    chat_jid TEXT NOT NULL,
    msg_id TEXT NOT NULL,
    sender_jid TEXT,
    from_me INTEGER NOT NULL DEFAULT 0,
    starred_at INTEGER NOT NULL,
    PRIMARY KEY (chat_jid, msg_id)
);

CREATE TABLE polls (
    chat_jid TEXT NOT NULL,
    msg_id TEXT NOT NULL,
    sender_jid TEXT,
    question TEXT NOT NULL,
    options_json TEXT NOT NULL,
    selectable_count INTEGER NOT NULL DEFAULT 1,
    created_ts INTEGER NOT NULL,
    PRIMARY KEY (chat_jid, msg_id)
);

CREATE TABLE poll_votes (
    chat_jid TEXT NOT NULL,
    poll_msg_id TEXT NOT NULL,
    voter_jid TEXT NOT NULL,
    vote_msg_id TEXT NOT NULL,
    selected_options_json TEXT NOT NULL,
    ts INTEGER NOT NULL,
    PRIMARY KEY (chat_jid, poll_msg_id, voter_jid)
);

CREATE TABLE messages_fts (
    rowid INTEGER PRIMARY KEY,
    text TEXT,
    media_caption TEXT,
    filename TEXT,
    chat_name TEXT,
    sender_name TEXT,
    display_text TEXT
);
