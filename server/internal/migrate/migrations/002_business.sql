-- Business records for one default conversation, a durable turn, and a Philo
-- business cursor. An owner id is not a Makez workspace id. Nothing here
-- deletes idempotency rows or chooses how long they are kept.
-- Historical session rows stay. is_default defaults to false, so existing
-- rows are not elected by updated_at or created_at.

CREATE TABLE owners (
    id TEXT PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE actors (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    created_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE agents ADD COLUMN owner_id TEXT REFERENCES owners (id);

ALTER TABLE sessions ADD COLUMN is_default BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE sessions ADD COLUMN business_high_water BIGINT NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX sessions_one_default_per_agent
    ON sessions (agent_id)
    WHERE is_default;

CREATE TABLE turns (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX turns_conversation_idx ON turns (conversation_id, created_at);

-- Scope is (owner, conversation, operation kind, target, client key).
-- The client key is not the turn id. request_hash_version 1 is this
-- document's version, not Makez's task-hash version. No expiry column.
CREATE TABLE idempotency_accepts (
    owner_id TEXT NOT NULL REFERENCES owners (id),
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    operation_kind TEXT NOT NULL,
    target_ref TEXT NOT NULL DEFAULT '',
    idempotency_key TEXT NOT NULL,
    request_hash TEXT NOT NULL,
    request_hash_version INTEGER NOT NULL DEFAULT 1 CHECK (request_hash_version = 1),
    turn_id TEXT REFERENCES turns (id),
    actor_id TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (owner_id, conversation_id, operation_kind, target_ref, idempotency_key)
);

ALTER TABLE messages ADD COLUMN turn_id TEXT REFERENCES turns (id);

CREATE INDEX messages_turn_idx ON messages (turn_id);

-- Cursor is per conversation. It is not messages.seq and not a Makez id.
CREATE TABLE business_events (
    id TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    cursor BIGINT NOT NULL,
    event_type TEXT NOT NULL,
    message_id TEXT,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    UNIQUE (conversation_id, cursor)
);

CREATE INDEX business_events_conversation_cursor_idx
    ON business_events (conversation_id, cursor);

CREATE TABLE wake_intents (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    turn_id TEXT NOT NULL REFERENCES turns (id),
    kind TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE outbox (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    turn_id TEXT REFERENCES turns (id),
    kind TEXT NOT NULL,
    payload JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL
);

-- One personal logical session per conversation and harness pin.
-- One worker logical session per worker run and harness pin.
-- Attempt is not part of either key.
CREATE TABLE logical_native_sessions (
    id TEXT PRIMARY KEY,
    owner_id TEXT NOT NULL REFERENCES owners (id),
    scope TEXT NOT NULL CHECK (scope IN ('personal', 'worker')),
    conversation_id TEXT REFERENCES sessions (id),
    worker_run_ref TEXT,
    harness_pin TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    CHECK (
        (scope = 'personal' AND conversation_id IS NOT NULL AND worker_run_ref IS NULL)
        OR
        (scope = 'worker' AND conversation_id IS NULL AND worker_run_ref IS NOT NULL)
    )
);

CREATE UNIQUE INDEX logical_native_personal_idx
    ON logical_native_sessions (owner_id, conversation_id, harness_pin)
    WHERE scope = 'personal';

CREATE UNIQUE INDEX logical_native_worker_idx
    ON logical_native_sessions (owner_id, worker_run_ref, harness_pin)
    WHERE scope = 'worker';

-- Dedup for the explicit fixture business source only. Production does not
-- ingest Makez from this table.
CREATE TABLE fixture_business_dedup (
    conversation_id TEXT NOT NULL REFERENCES sessions (id),
    event_key TEXT NOT NULL,
    business_event_id TEXT NOT NULL REFERENCES business_events (id),
    PRIMARY KEY (conversation_id, event_key)
);
