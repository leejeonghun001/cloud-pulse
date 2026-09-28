package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// MarkAlertSent records that an alert for (hostID, month, level) has been
// sent. It returns first=true if this call actually inserted the row
// (i.e. no alert for that host/month/level had been recorded before);
// first=false if it was already recorded, so the caller should not
// re-notify.
func (db *DB) MarkAlertSent(ctx context.Context, hostID, month string, level models.EgressLevel) (bool, error) {
	res, err := db.sql.ExecContext(ctx, `
		INSERT OR IGNORE INTO alerts_sent (host_id, month, level, sent_at)
		VALUES (?, ?, ?, ?)
	`, hostID, month, string(level), time.Now().Unix())
	if err != nil {
		return false, fmt.Errorf("storage: mark alert sent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("storage: mark alert sent: rows affected: %w", err)
	}
	return n == 1, nil
}
