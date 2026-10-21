-- Which scheduled slots (the nightly receipt collection at 9/10/11 PM India time)
-- have already been queued, so several workers never queue one twice.
CREATE TABLE IF NOT EXISTS df_schedule_fire (
    slot      text PRIMARY KEY,
    fired_at  timestamptz NOT NULL DEFAULT now()
);
