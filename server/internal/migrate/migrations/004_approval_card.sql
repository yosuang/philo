-- An approval card binds one action snapshot and the requester actor taken
-- from the server-configured bearer. Editing the action marks that card
-- invalid and requires a new card. This is not a Makez run binding, a lease,
-- or an owner-to-workspace map. Historical rows are not given a requester.

ALTER TABLE approvals DROP CONSTRAINT approvals_status_check;
ALTER TABLE approvals ADD CONSTRAINT approvals_status_check
    CHECK (status IN ('pending', 'approved', 'rejected', 'invalid'));

ALTER TABLE approvals ADD COLUMN requester_actor_id TEXT REFERENCES actors (id);
ALTER TABLE approvals ADD COLUMN action_snapshot JSONB;

DROP INDEX approvals_message_id_idx;
CREATE INDEX approvals_message_id_idx ON approvals (message_id);

CREATE UNIQUE INDEX approvals_one_pending_message
    ON approvals (message_id)
    WHERE status = 'pending';
