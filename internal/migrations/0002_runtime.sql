-- Tables the Go application adds on top of the Django schema.

-- Browser sessions (the Django project used django_session; its payload is not readable here).
CREATE TABLE IF NOT EXISTS df_session (
    key         text PRIMARY KEY,
    user_id     integer REFERENCES auth_user(id) ON DELETE CASCADE,
    data        jsonb NOT NULL DEFAULT '{}'::jsonb,
    expires_at  timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS df_session_expires ON df_session (expires_at);

-- Background jobs (the Celery + Redis queue of the Django project).
CREATE TABLE IF NOT EXISTS df_job (
    id            bigserial PRIMARY KEY,
    kind          text NOT NULL,
    args          jsonb NOT NULL DEFAULT '{}'::jsonb,
    status        text NOT NULL DEFAULT 'queued',
    run_at        timestamptz NOT NULL DEFAULT now(),
    attempts      integer NOT NULL DEFAULT 0,
    locked_by     text NOT NULL DEFAULT '',
    heartbeat_at  timestamptz,
    last_error    text NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz
);
CREATE INDEX IF NOT EXISTS df_job_pick ON df_job (status, run_at);
