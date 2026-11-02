CREATE TABLE agent_task_requests (
    id           uuid PRIMARY KEY,
    task_type    text NOT NULL,
    entity_type  text NOT NULL,
    entity_id    text NOT NULL,
    payload      jsonb NOT NULL DEFAULT '{}'::jsonb,
    requested_by text,
    created_at   timestamptz NOT NULL DEFAULT now()
);

-- This table is an append-only outbox. Applications insert a task in the same
-- transaction as the business change that makes the task necessary.
