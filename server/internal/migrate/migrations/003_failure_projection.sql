-- Projection of one captured Makez failure onto a Philo turn.
-- The public replay identity is (run_id, seq) from the captured log.
-- source_offset is not a column: the public bytes do not carry it, and
-- this migration does not decide whether a later resume reuses it.
-- makez_workspace_id is the workspace observed on the task record.
-- It is not owners.id and not a client-supplied tenant.

ALTER TABLE turns
    ADD COLUMN makez_workspace_id TEXT,
    ADD COLUMN makez_task_id TEXT,
    ADD COLUMN makez_run_id TEXT;

ALTER TABLE outbox ADD COLUMN published_at TIMESTAMPTZ;

-- applied_public_seq is the Makez public log seq that has been stored.
-- processing_cursor is the raw run.cursor string, such as stdout:378,stderr:0.
-- That string is not applied_public_seq and not sessions.business_high_water.
CREATE TABLE source_checkpoints (
    workspace_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    applied_public_seq BIGINT NOT NULL,
    processing_cursor TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, task_id, run_id)
);

CREATE TABLE source_log_dedup (
    run_id TEXT NOT NULL,
    public_seq BIGINT NOT NULL,
    payload_sha256 TEXT NOT NULL,
    failure_text TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, public_seq)
);

-- Dedup of a terminal task observation. The tuple is the one in the
-- captured contract sentence, not a new Makez publish protocol.
CREATE TABLE terminal_observations (
    workspace_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL,
    message_id TEXT NOT NULL REFERENCES messages (id),
    business_event_id TEXT,
    outbox_id TEXT,
    PRIMARY KEY (workspace_id, task_id, run_id, state, reason)
);

-- acked_at is a Philo-local receipt written after COMMIT.
-- It is not an acknowledgement Makez defined and not a source_offset.
CREATE TABLE projection_receipts (
    workspace_id TEXT NOT NULL,
    task_id TEXT NOT NULL,
    run_id TEXT NOT NULL,
    state TEXT NOT NULL,
    reason TEXT NOT NULL,
    acked_at TIMESTAMPTZ,
    PRIMARY KEY (workspace_id, task_id, run_id, state, reason)
);
