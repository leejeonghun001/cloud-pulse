-- Agent-collected provider cost snapshots record which host collected
-- them; '' means the hub's own collector produced the row.
ALTER TABLE cloud_cost_snapshots ADD COLUMN source_host TEXT NOT NULL DEFAULT '';
