package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// MarkAlertSent records that an alert for (hostID, month, dir, level) has
// been sent. It returns first=true if this call actually inserted the row
// (i.e. no alert for that host/month/direction/level had been recorded
// before); first=false if it was already recorded, so the caller should
// not re-notify. Outbound and inbound alerts are tracked independently.
func (db *DB) MarkAlertSent(ctx context.Context, hostID, month string, dir models.Direction, level models.EgressLevel) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `
		INSERT OR IGNORE INTO alerts_sent (host_id, month, direction, level, sent_at)
		VALUES (?, ?, ?, ?, ?)
	`, hostID, month, string(dir), string(level), time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("storage: mark alert sent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("storage: mark alert sent: rows affected: %w", err)
	}
	return n == 1, nil
}
