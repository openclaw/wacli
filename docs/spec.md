# Architecture and compatibility

Read when: changing command boundaries, storage, synchronization, or protocol handling.

`wacli` pairs as a WhatsApp linked device through `whatsmeow`, mirrors messages into a local SQLite index, and provides offline search alongside authenticated send and chat-management commands. The [overview](overview.md) and command pages describe the current CLI surface; this document records the boundaries behind it.

## Ownership

| Package | Responsibility |
| --- | --- |
| `cmd/wacli` | Cobra commands, flags, validation, output, recipient selection, and local follow-process delegation. |
| `internal/app` | Sync lifecycle, event persistence, backfill, media workers, webhooks, and app-state recovery. |
| `internal/wa` | Whatsmeow client access, JID resolution, protocol construction, and message parsing. |
| `internal/store` | Local schema, migrations, message queries, identity repair, and search indexing. |
| `internal/config`, `internal/lock` | Account/store selection and exclusive writer ownership. |
| `internal/out`, `internal/fsutil`, `internal/sqliteutil`, `internal/pathutil` | Output, private files, SQLite paths, and path sanitization. |

## Storage layout

Each store owns two databases: `session.db` contains whatsmeow-managed identities, keys, and protocol state; `wacli.db` contains wacli's searchable mirror. Keep their schemas and lifecycle separate. Named accounts select isolated stores rather than combining account data in one database.

The other store files include downloaded `media/`, an exclusive-writer `LOCK`, and the follow process's `HEARTBEAT`. The heartbeat records observed activity at most once per minute; it is not a process-liveness or keepalive-health signal. Files containing account state use owner-only permissions.

Store selection and the supported legacy Linux directory fallback are documented in [accounts](accounts.md). Local row removal, retention, and statistics are documented in [store](store.md).

## Locking and read-only access

Commands that write local state or access the live WhatsApp session acquire the per-store lock. `--lock-wait` controls bounded waiting. `--read-only` and `WACLI_READONLY=1` reject intentional writes; local readers can inspect the mirror while sync owns the lock.

After `sync --follow` completes startup, its local delegate socket accepts supported send and read-state operations. The follow process retains ownership of the session and store lock. The invoking command still validates writable mode and preserves its normal result format. See [send](send.md) and [chats](chats.md) for supported operations and upgrade constraints.

## Authentication and synchronization

`auth` handles explicit QR or phone-number pairing and then bootstrap sync. `sync` requires an existing session and never displays a QR code. Pairing states that cannot be completed safely, including passkey verification, return an actionable error; see [auth](auth.md).

`internal/app` routes live and history events through the message parser and persistence layer. Message upserts use `(chat_jid, msg_id)` identities so replay does not create duplicate messages. Status broadcasts have their own table. Location, poll, star, and call metadata have separate records where their query and update semantics require them.

Follow mode handles connection loss and bounded reconnection. One-shot sync waits for idle and drains queued media on successful completion. Context cancellation bounds network operations; follow-mode shutdown can send final unavailable presence before closing its detached socket.

App-state writes span whatsmeow's session state and the local mirror. Persisted recovery intents and ordered event persistence keep interrupted writes replayable; these are upgrade and crash-recovery contracts, not disposable compatibility shims.

History is best-effort: the primary phone decides what older messages are available. Backfill uses real stored message identities as protocol anchors. See [history](history.md), [sync](sync.md), and [media](media.md) for limits and operational behavior.

## Schema and search

`internal/store/schema.sql` defines new-store tables. Ordered migrations preserve existing stores, including earlier identity, tombstone, and app-state layouts. Static queries are generated from `internal/store/sqlc/queries.sql` with `pnpm generate:sqlc`; edit those sources rather than generated `storedb` files. Dynamic search/filter queries remain in `internal/store`.

FTS5 uses a separate `messages_fts` virtual table synchronized by insert, update, and delete triggers. It indexes message text, display text, captions, filenames, and chat/sender names. Tombstones remain addressable by direct message lookup but are excluded from ordinary list/search/export results and the search index. Builds without FTS5 retain the slower `LIKE` search fallback.

The standard build enables `sqlite_fts5` and requires cgo. Both FTS and non-FTS test suites are required; neither path is expendable. See [installation](install.md) for build prerequisites.

## Output and integrations

Human-readable tables are the default. `--json` returns the established `success`, `data`, and `error` envelope. Long-running commands can emit NDJSON lifecycle events on stderr with `--events`; progress, warnings, and errors must not corrupt primary stdout data.

Webhooks run on a bounded worker and preserve their documented payloads and event selection. Companions can also read `wacli.db` in read-only mode. See [integrations](integrations.md) for schemas and supported access patterns.

## Compatibility boundaries

Public command names, flags, environment variables, JSON fields, account configuration, and persisted data formats are contracts. Preserve them when reorganizing internals, and document intentional compatibility changes in the changelog. Keep source changes within the Go minimum in `go.mod`; the preferred toolchain is a separate build and release pin.

Complete WhatsApp message-type parity and guaranteed full-history recovery are not promised. Unsupported payload diagnostics identify content that was not extracted without inventing text or inferring missing protocol identities.

For development gates, follow the repository's `AGENTS.md` and `Makefile`. Official artifact preparation and verification are described in [release](release.md).
