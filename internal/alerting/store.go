package alerting

import (
	"context"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// Store is the subset of hub.Store the alerting engine depends on. It is
// declared independently here (rather than importing internal/hub) so
// internal/alerting has no dependency on internal/hub at all;
// *storage.DB and internal/hub's fakeStore both already satisfy this
// interface structurally since it is a strict subset of hub.Store's
// method signatures for the same names.
type Store interface {
	// ListAlertRules returns every configured alert rule.
	ListAlertRules(ctx context.Context) ([]models.AlertRule, error)
	// GetAlertRule returns one alert rule by id, or models.ErrNotFound.
	GetAlertRule(ctx context.Context, id int64) (models.AlertRule, error)

	// GetNotifyChannel returns one channel by id, or models.ErrNotFound.
	GetNotifyChannel(ctx context.Context, id int64) (models.NotifyChannel, error)

	// GetAlertState returns the persisted state-machine row for
	// (ruleID, hostID), defaulting to State: models.AlertStateOK if no
	// row exists yet.
	GetAlertState(ctx context.Context, ruleID int64, hostID string) (models.AlertState, error)
	// SetAlertState upserts the state-machine row for (st.RuleID,
	// st.HostID).
	SetAlertState(ctx context.Context, st models.AlertState) error
	// DeleteAlertStatesForRule removes every state-machine row for
	// ruleID.
	DeleteAlertStatesForRule(ctx context.Context, ruleID int64) error

	// CreateAlertEvent inserts ev (ignoring ev.ID) and returns the row
	// with its assigned ID populated.
	CreateAlertEvent(ctx context.Context, ev models.AlertEvent) (models.AlertEvent, error)
	// UpdateAlertEvent replaces the stored event matching ev.ID with ev
	// in full, or returns models.ErrNotFound.
	UpdateAlertEvent(ctx context.Context, ev models.AlertEvent) error
	// GetActiveAlertEvent returns the currently-firing event for
	// (ruleID, hostID), or models.ErrNotFound if none is firing.
	GetActiveAlertEvent(ctx context.Context, ruleID int64, hostID string) (models.AlertEvent, error)
	// GetAlertEvent returns one event by id, or models.ErrNotFound. Used
	// by the delivery worker to re-fetch an event's current Deliveries
	// immediately before appending a new one (see notify.go's
	// recordDelivery), since notifyEvent enqueues one delivery job per
	// channel from a single shared event snapshot — appending to that
	// stale snapshot instead of the current row would silently drop
	// every delivery but the last one recorded for a multi-channel
	// rule.
	GetAlertEvent(ctx context.Context, id int64) (models.AlertEvent, error)

	// QuerySeries returns a time series for hostID over [from, to] at a
	// resolution chosen by the implementation based on the range, used
	// for sustained-window evaluation.
	QuerySeries(ctx context.Context, hostID string, from, to int64) (models.Series, error)
}
