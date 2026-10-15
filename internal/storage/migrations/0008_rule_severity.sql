-- Rule severity is persisted so notification urgency is configurable and
-- historical events retain the urgency that applied when they fired.
ALTER TABLE alert_rules ADD COLUMN severity TEXT NOT NULL DEFAULT 'warning';
UPDATE alert_rules SET severity = 'critical' WHERE metric IN ('host_down', 'update_batch_failed');
-- 0004 seeded two egress rules whose names already promise "critical" and
-- "exceeded". While the operator has kept those seeded names, the rules
-- become critical so the badge agrees with the name. Their past events
-- stay warning below: that is what they were delivered as.
UPDATE alert_rules SET severity = 'critical'
WHERE metric = 'egress_out_pct'
  AND name IN ('Outbound traffic 95% (critical)', 'Outbound traffic 100% (exceeded)');

ALTER TABLE alert_events ADD COLUMN severity TEXT NOT NULL DEFAULT 'warning';
UPDATE alert_events SET severity = 'critical' WHERE metric IN ('host_down', 'update_batch_failed');
