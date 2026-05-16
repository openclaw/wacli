-- name: UpsertChat :exec
INSERT INTO chats(jid, kind, name, last_message_ts)
VALUES(?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    kind=excluded.kind,
    name=CASE WHEN excluded.name IS NOT NULL AND excluded.name != '' THEN excluded.name ELSE chats.name END,
    last_message_ts=CASE WHEN excluded.last_message_ts > COALESCE(chats.last_message_ts, 0) THEN excluded.last_message_ts ELSE chats.last_message_ts END;

-- name: GetChat :one
SELECT jid, kind, COALESCE(name,''), COALESCE(last_message_ts,0), COALESCE(archived,0), COALESCE(pinned,0), COALESCE(muted_until,0), COALESCE(unread,0)
FROM chats
WHERE jid = ?;

-- name: SetChatArchived :exec
INSERT INTO chats(jid, kind, archived)
VALUES(?, 'unknown', ?)
ON CONFLICT(jid) DO UPDATE SET archived=excluded.archived;

-- name: SetChatArchivedAndUnpin :exec
INSERT INTO chats(jid, kind, archived)
VALUES(?, 'unknown', ?)
ON CONFLICT(jid) DO UPDATE SET archived=excluded.archived, pinned=0;

-- name: SetChatPinned :exec
INSERT INTO chats(jid, kind, pinned)
VALUES(?, 'unknown', ?)
ON CONFLICT(jid) DO UPDATE SET pinned=excluded.pinned;

-- name: SetChatMutedUntil :exec
INSERT INTO chats(jid, kind, muted_until)
VALUES(?, 'unknown', ?)
ON CONFLICT(jid) DO UPDATE SET muted_until=excluded.muted_until;

-- name: SetChatUnread :exec
INSERT INTO chats(jid, kind, unread)
VALUES(?, 'unknown', ?)
ON CONFLICT(jid) DO UPDATE SET unread=excluded.unread;

-- name: DeletePollVotesForChat :exec
DELETE FROM poll_votes WHERE chat_jid = ?;

-- name: DeletePollsForChat :exec
DELETE FROM polls WHERE chat_jid = ?;

-- name: DeleteStarredForChat :exec
DELETE FROM starred WHERE chat_jid = ?;

-- name: DeleteChat :exec
DELETE FROM chats WHERE jid = ?;

-- name: CountChatMessages :one
SELECT COUNT(1) FROM messages WHERE chat_jid = ?;

-- name: ListContacts :many
SELECT c.jid,
       COALESCE(c.phone,'') AS phone,
       COALESCE(NULLIF(a.alias,''), '') AS alias,
       COALESCE(NULLIF(c.system_name,''), '') AS system_name,
       COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), '') AS name,
       c.updated_at
FROM contacts c
LEFT JOIN contact_aliases a ON a.jid = c.jid
ORDER BY COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), c.jid)
LIMIT ?;

-- name: GetContact :one
SELECT c.jid,
       COALESCE(c.phone,'') AS phone,
       COALESCE(NULLIF(a.alias,''), '') AS alias,
       COALESCE(NULLIF(c.system_name,''), '') AS system_name,
       COALESCE(NULLIF(a.alias,''), NULLIF(c.system_name,''), NULLIF(c.full_name,''), NULLIF(c.push_name,''), NULLIF(c.business_name,''), NULLIF(c.first_name,''), '') AS name,
       c.updated_at
FROM contacts c
LEFT JOIN contact_aliases a ON a.jid = c.jid
WHERE c.jid = ?;

-- name: SetSystemName :execrows
UPDATE contacts SET system_name = ?, updated_at = ? WHERE jid = ?;

-- name: ClearAllSystemNames :execrows
UPDATE contacts SET system_name = NULL, updated_at = ? WHERE system_name IS NOT NULL AND system_name != '';

-- name: CountSystemNames :one
SELECT COUNT(1) FROM contacts WHERE system_name IS NOT NULL AND system_name != '';

-- name: ListTags :many
SELECT tag FROM contact_tags WHERE jid = ? ORDER BY tag;

-- name: UpsertContact :exec
INSERT INTO contacts(jid, phone, push_name, full_name, first_name, business_name, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(jid) DO UPDATE SET
    phone=COALESCE(NULLIF(excluded.phone,''), contacts.phone),
    push_name=COALESCE(NULLIF(excluded.push_name,''), contacts.push_name),
    full_name=COALESCE(NULLIF(excluded.full_name,''), contacts.full_name),
    first_name=COALESCE(NULLIF(excluded.first_name,''), contacts.first_name),
    business_name=COALESCE(NULLIF(excluded.business_name,''), contacts.business_name),
    updated_at=excluded.updated_at;

-- name: SetAlias :exec
INSERT INTO contact_aliases(jid, alias, notes, updated_at)
VALUES (?, ?, NULL, ?)
ON CONFLICT(jid) DO UPDATE SET alias=excluded.alias, updated_at=excluded.updated_at;

-- name: RemoveAlias :exec
DELETE FROM contact_aliases WHERE jid = ?;

-- name: AddTag :exec
INSERT INTO contact_tags(jid, tag, updated_at)
VALUES(?, ?, ?)
ON CONFLICT(jid, tag) DO UPDATE SET updated_at=excluded.updated_at;

-- name: RemoveTag :exec
DELETE FROM contact_tags WHERE jid = ? AND tag = ?;

-- name: UpsertGroup :exec
INSERT INTO groups(jid, name, owner_jid, created_ts, left_at, updated_at)
VALUES (?, ?, ?, ?, NULL, ?)
ON CONFLICT(jid) DO UPDATE SET
    name=COALESCE(NULLIF(excluded.name,''), groups.name),
    owner_jid=COALESCE(NULLIF(excluded.owner_jid,''), groups.owner_jid),
    created_ts=COALESCE(NULLIF(excluded.created_ts,0), groups.created_ts),
    left_at=NULL,
    updated_at=excluded.updated_at;

-- name: UpsertGroupWithHierarchy :exec
INSERT INTO groups(jid, name, owner_jid, created_ts, is_parent, linked_parent_jid, left_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, NULL, ?)
ON CONFLICT(jid) DO UPDATE SET
    name=COALESCE(NULLIF(excluded.name,''), groups.name),
    owner_jid=COALESCE(NULLIF(excluded.owner_jid,''), groups.owner_jid),
    created_ts=COALESCE(NULLIF(excluded.created_ts,0), groups.created_ts),
    is_parent=excluded.is_parent,
    linked_parent_jid=excluded.linked_parent_jid,
    left_at=NULL,
    updated_at=excluded.updated_at;

-- name: MarkGroupLeft :exec
UPDATE groups SET left_at = ?, updated_at = ? WHERE jid = ?;

-- name: ListJoinedGroupJIDs :many
SELECT jid FROM groups WHERE left_at IS NULL;

-- name: DeleteGroupParticipants :exec
DELETE FROM group_participants WHERE group_jid = ?;

-- name: InsertGroupParticipant :exec
INSERT INTO group_participants(group_jid, user_jid, role, updated_at)
VALUES(?, ?, ?, ?);

-- name: DeleteGroup :exec
DELETE FROM groups WHERE jid = ?;

-- name: ListLeftGroups :many
SELECT jid, COALESCE(name,''), COALESCE(owner_jid,''), is_parent, COALESCE(linked_parent_jid,''), COALESCE(created_ts,0), COALESCE(left_at,0), updated_at
FROM groups
WHERE left_at IS NOT NULL
ORDER BY left_at DESC;

-- name: DeleteLeftGroups :execrows
DELETE FROM groups WHERE left_at IS NOT NULL;

-- name: DeleteLeftGroupsOlderThan :execrows
DELETE FROM groups WHERE left_at IS NOT NULL AND left_at < ?;

-- name: MarkMessageRevoked :execrows
UPDATE messages
SET revoked = 1,
    text = NULL,
    display_text = ?,
    buttons = NULL,
    media_type = NULL,
    media_caption = NULL,
    filename = NULL,
    mime_type = NULL,
    direct_path = NULL,
    media_key = NULL,
    file_sha256 = NULL,
    file_enc_sha256 = NULL,
    file_length = NULL,
    local_path = NULL,
    downloaded_at = NULL,
    edited = 0,
    edited_ts = 0
WHERE chat_jid = ? AND msg_id = ?;

-- name: MarkMessageDeletedForMe :execrows
UPDATE messages
SET deleted_for_me = 1,
    text = NULL,
    display_text = ?,
    buttons = NULL,
    media_type = NULL,
    media_caption = NULL,
    filename = NULL,
    mime_type = NULL,
    direct_path = NULL,
    media_key = NULL,
    file_sha256 = NULL,
    file_enc_sha256 = NULL,
    file_length = NULL,
    local_path = NULL,
    downloaded_at = NULL,
    edited = 0,
    edited_ts = 0
WHERE chat_jid = ? AND msg_id = ?;

-- name: UpdateMessageText :execrows
UPDATE messages
SET text = ?,
    display_text = ?,
    buttons = NULL,
    media_type = NULL,
    media_caption = NULL,
    filename = NULL,
    mime_type = NULL,
    direct_path = NULL,
    media_key = NULL,
    file_sha256 = NULL,
    file_enc_sha256 = NULL,
    file_length = NULL,
    local_path = NULL,
    downloaded_at = NULL,
    revoked = 0,
    deleted_for_me = 0,
    edited = 1,
    edited_ts = strftime('%s', 'now')
WHERE chat_jid = ? AND msg_id = ?;

-- name: GetMessage :one
SELECT m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), m.revoked, m.deleted_for_me, COALESCE(m.buttons,''), ''
FROM messages m
LEFT JOIN chats c ON c.jid = m.chat_jid
LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
WHERE m.chat_jid = ? AND m.msg_id = ?;

-- name: CountMessages :one
SELECT COUNT(1) FROM messages;

-- name: GetOldestMessageInfo :one
SELECT m.chat_jid, m.msg_id, m.ts, m.from_me, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,'')
FROM messages m
WHERE m.chat_jid = ?
ORDER BY m.ts ASC, m.rowid ASC
LIMIT 1;

-- name: GetLatestMessageInfo :one
SELECT m.chat_jid, m.msg_id, m.ts, m.from_me, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,'')
FROM messages m
WHERE m.chat_jid = ?
ORDER BY m.ts DESC, m.rowid DESC
LIMIT 1;

-- name: MessageContextBefore :many
SELECT m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), m.revoked, m.deleted_for_me, COALESCE(m.buttons,''), ''
FROM messages m
LEFT JOIN chats c ON c.jid = m.chat_jid
LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
WHERE m.chat_jid = ? AND m.revoked = 0 AND m.deleted_for_me = 0 AND (m.ts < ? OR (m.ts = ? AND m.rowid < ?))
ORDER BY m.ts DESC, m.rowid DESC
LIMIT ?;

-- name: MessageContextAfter :many
SELECT m.rowid, m.chat_jid, COALESCE(c.name,''), m.msg_id, COALESCE(m.sender_jid,''), COALESCE(m.sender_name,''), m.ts, m.from_me, COALESCE(m.text,''), COALESCE(m.display_text,''), m.is_forwarded, m.forwarding_score, COALESCE(m.reaction_to_id,''), COALESCE(m.reaction_emoji,''), COALESCE(m.media_type,''), COALESCE(m.media_caption,''), COALESCE(m.filename,''), COALESCE(m.mime_type,''), COALESCE(m.direct_path,''), COALESCE(m.local_path,''), COALESCE(m.downloaded_at,0), CASE WHEN s.msg_id IS NULL THEN 0 ELSE 1 END, COALESCE(s.starred_at,0), m.revoked, m.deleted_for_me, COALESCE(m.buttons,''), ''
FROM messages m
LEFT JOIN chats c ON c.jid = m.chat_jid
LEFT JOIN starred s ON s.chat_jid = m.chat_jid AND s.msg_id = m.msg_id
WHERE m.chat_jid = ? AND m.revoked = 0 AND m.deleted_for_me = 0 AND (m.ts > ? OR (m.ts = ? AND m.rowid > ?))
ORDER BY m.ts ASC, m.rowid ASC
LIMIT ?;

-- name: SetStarredDelete :exec
DELETE FROM starred WHERE chat_jid = ? AND msg_id = ?;

-- name: SetStarredUpsert :exec
INSERT INTO starred(chat_jid, msg_id, sender_jid, from_me, starred_at)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
    sender_jid=COALESCE(NULLIF(excluded.sender_jid,''), starred.sender_jid),
    from_me=excluded.from_me,
    starred_at=excluded.starred_at;

-- name: Stats :one
SELECT
    (SELECT COUNT(*) FROM messages),
    (SELECT COUNT(*) FROM chats),
    (SELECT COUNT(*) FROM contacts),
    (SELECT COUNT(*) FROM groups),
    COALESCE((SELECT MAX(ts) FROM messages), 0);

-- name: GetMediaDownloadInfo :one
SELECT m.chat_jid,
       COALESCE(c.name,''),
       m.msg_id,
       COALESCE(m.media_type,''),
       COALESCE(m.filename,''),
       COALESCE(m.mime_type,''),
       COALESCE(m.direct_path,''),
       m.media_key,
       m.file_sha256,
       m.file_enc_sha256,
       COALESCE(m.file_length,0),
       COALESCE(m.local_path,''),
       COALESCE(m.downloaded_at,0)
FROM messages m
LEFT JOIN chats c ON c.jid = m.chat_jid
WHERE m.chat_jid = ? AND m.msg_id = ?;

-- name: MarkMediaDownloaded :exec
UPDATE messages SET local_path = ?, downloaded_at = ? WHERE chat_jid = ? AND msg_id = ?;

-- name: UpsertPoll :exec
INSERT INTO polls (chat_jid, msg_id, sender_jid, question, options_json, selectable_count, created_ts)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_jid, msg_id) DO UPDATE SET
    sender_jid = excluded.sender_jid,
    question = excluded.question,
    options_json = excluded.options_json,
    selectable_count = excluded.selectable_count,
    created_ts = excluded.created_ts;

-- name: GetPoll :one
SELECT p.chat_jid, p.msg_id, COALESCE(p.sender_jid,''), p.question, p.options_json, p.selectable_count, p.created_ts
FROM polls p
LEFT JOIN messages m ON m.chat_jid = p.chat_jid AND m.msg_id = p.msg_id
WHERE p.chat_jid = ? AND p.msg_id = ?
  AND (m.msg_id IS NULL OR (m.revoked = 0 AND m.deleted_for_me = 0));

-- name: FindPollByMsgID :one
SELECT p.chat_jid, p.msg_id, COALESCE(p.sender_jid,''), p.question, p.options_json, p.selectable_count, p.created_ts
FROM polls p
LEFT JOIN messages m ON m.chat_jid = p.chat_jid AND m.msg_id = p.msg_id
WHERE p.msg_id = ?
  AND (m.msg_id IS NULL OR (m.revoked = 0 AND m.deleted_for_me = 0))
ORDER BY p.created_ts DESC
LIMIT 1;

-- name: ListPolls :many
SELECT p.chat_jid, p.msg_id, COALESCE(p.sender_jid,''), p.question, p.options_json, p.selectable_count, p.created_ts
FROM polls p
LEFT JOIN messages m ON m.chat_jid = p.chat_jid AND m.msg_id = p.msg_id
WHERE (m.msg_id IS NULL OR (m.revoked = 0 AND m.deleted_for_me = 0))
  AND (? = '' OR p.chat_jid = ?)
ORDER BY p.created_ts DESC, p.msg_id DESC
LIMIT ? OFFSET ?;

-- name: PollOptions :one
SELECT options_json FROM polls WHERE chat_jid = ? AND msg_id = ?;

-- name: UpsertPollVote :exec
INSERT INTO poll_votes (chat_jid, poll_msg_id, voter_jid, vote_msg_id, selected_options_json, ts)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(chat_jid, poll_msg_id, voter_jid) DO UPDATE SET
    vote_msg_id = excluded.vote_msg_id,
    selected_options_json = excluded.selected_options_json,
    ts = excluded.ts
WHERE excluded.ts >= poll_votes.ts;

-- name: DeletePollVote :exec
DELETE FROM poll_votes
WHERE chat_jid = ? AND poll_msg_id = ? AND voter_jid = ? AND ts <= ?;

-- name: ListPollVotes :many
SELECT chat_jid, poll_msg_id, voter_jid, vote_msg_id, selected_options_json, ts
FROM poll_votes
WHERE chat_jid = ? AND poll_msg_id = ?
ORDER BY ts ASC, voter_jid ASC;

-- name: DeletePollVotesForPoll :exec
DELETE FROM poll_votes WHERE chat_jid = ? AND poll_msg_id = ?;

-- name: DeletePoll :exec
DELETE FROM polls WHERE chat_jid = ? AND msg_id = ?;
