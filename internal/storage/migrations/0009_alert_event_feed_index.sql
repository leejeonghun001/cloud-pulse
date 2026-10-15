-- Active-alert and filtered event feeds select by state then order by newest.
-- The previous state-only index still required a temp sort at fleet scale.
CREATE INDEX idx_alert_events_state_started_at ON alert_events (state, started_at DESC);
