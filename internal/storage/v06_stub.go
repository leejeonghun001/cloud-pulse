// v06_stub.go implements the v0.6.0 additions to hub.Store (cloud
// billing snapshots, remote agent update jobs, network cost pricing
// plans/host assignments, and the audit log) against the schema in
// migrations/0005_v06.sql. Named "_stub" because it is written once by
// the prep stage purely so storage.DB keeps satisfying hub.Store as the
// interface grows; the owning v0.6.0 stages (billing, remote-update,
// network-cost) may replace/extend these implementations, but must not
// change the Store interface's method signatures without updating
// server.go and every other stage's fakes in lockstep — see
// notes/v06-prep.md for exact per-stage file ownership.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// --- Cloud billing (SPEC-v0.6 §1) ---

// GetCloudCostSnapshot returns the persisted snapshot for provider, or a
// zero-value models.CloudCostSnapshot{Provider: provider, Status:
// models.CloudBillingNotConfigured} with a nil error if no row exists
// yet.
func (db *DB) GetCloudCostSnapshot(ctx context.Context, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT status, status_detail, currency, mtd_cost, forecast_cost, forecast_method,
		       account_level, per_resource, collected_at, last_success_at, last_attempt_at
		FROM cloud_cost_snapshots WHERE provider = ?
	`, string(provider))
	snap, err := scanCloudCostSnapshot(row.Scan, provider)
	if err != nil {
		if isNoRows(err) {
			return models.CloudCostSnapshot{Provider: provider, Status: models.CloudBillingNotConfigured}, nil
		}
		return models.CloudCostSnapshot{}, fmt.Errorf("storage: get cloud cost snapshot: %w", err)
	}
	return snap, nil
}

// ListCloudCostSnapshots returns every persisted provider snapshot,
// sorted by provider.
func (db *DB) ListCloudCostSnapshots(ctx context.Context) ([]models.CloudCostSnapshot, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT provider, status, status_detail, currency, mtd_cost, forecast_cost, forecast_method,
		       account_level, per_resource, collected_at, last_success_at, last_attempt_at
		FROM cloud_cost_snapshots ORDER BY provider
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list cloud cost snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.CloudCostSnapshot, 0)
	for rows.Next() {
		var provider string
		var snap models.CloudCostSnapshot
		var perResourceJSON string
		if err := rows.Scan(&provider, &snap.Status, &snap.StatusDetail, &snap.Currency, &snap.MTDCost,
			&snap.ForecastCost, &snap.ForecastMethod, &snap.AccountLevel, &perResourceJSON,
			&snap.CollectedAt, &snap.LastSuccessAt, &snap.LastAttemptAt); err != nil {
			return nil, fmt.Errorf("storage: list cloud cost snapshots: %w", err)
		}
		snap.Provider = models.CloudBillingProvider(provider)
		perResource, err := unmarshalPerResource(perResourceJSON)
		if err != nil {
			return nil, fmt.Errorf("storage: list cloud cost snapshots: unmarshal per_resource: %w", err)
		}
		snap.PerResource = perResource
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list cloud cost snapshots: %w", err)
	}
	return out, nil
}

// SetCloudCostSnapshot upserts the snapshot for snap.Provider.
func (db *DB) SetCloudCostSnapshot(ctx context.Context, snap models.CloudCostSnapshot) error {
	perResourceJSON, err := marshalPerResource(snap.PerResource)
	if err != nil {
		return fmt.Errorf("storage: set cloud cost snapshot: %w", err)
	}
	_, err = db.sql.ExecContext(ctx, `
		INSERT INTO cloud_cost_snapshots (
			provider, status, status_detail, currency, mtd_cost, forecast_cost, forecast_method,
			account_level, per_resource, collected_at, last_success_at, last_attempt_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			status = excluded.status,
			status_detail = excluded.status_detail,
			currency = excluded.currency,
			mtd_cost = excluded.mtd_cost,
			forecast_cost = excluded.forecast_cost,
			forecast_method = excluded.forecast_method,
			account_level = excluded.account_level,
			per_resource = excluded.per_resource,
			collected_at = excluded.collected_at,
			last_success_at = excluded.last_success_at,
			last_attempt_at = excluded.last_attempt_at
	`, string(snap.Provider), string(snap.Status), snap.StatusDetail, snap.Currency, snap.MTDCost,
		snap.ForecastCost, snap.ForecastMethod, snap.AccountLevel, perResourceJSON,
		snap.CollectedAt, snap.LastSuccessAt, snap.LastAttemptAt)
	if err != nil {
		return fmt.Errorf("storage: set cloud cost snapshot: %w", err)
	}
	return nil
}

func scanCloudCostSnapshot(scan func(dest ...any) error, provider models.CloudBillingProvider) (models.CloudCostSnapshot, error) {
	var snap models.CloudCostSnapshot
	var status string
	var perResourceJSON string
	if err := scan(&status, &snap.StatusDetail, &snap.Currency, &snap.MTDCost, &snap.ForecastCost,
		&snap.ForecastMethod, &snap.AccountLevel, &perResourceJSON, &snap.CollectedAt,
		&snap.LastSuccessAt, &snap.LastAttemptAt); err != nil {
		return models.CloudCostSnapshot{}, err
	}
	snap.Provider = provider
	snap.Status = models.CloudBillingStatus(status)
	perResource, err := unmarshalPerResource(perResourceJSON)
	if err != nil {
		return models.CloudCostSnapshot{}, fmt.Errorf("unmarshal per_resource: %w", err)
	}
	snap.PerResource = perResource
	return snap, nil
}

func marshalPerResource(m map[string]float64) (string, error) {
	if m == nil {
		m = map[string]float64{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal per_resource: %w", err)
	}
	return string(b), nil
}

func unmarshalPerResource(s string) (map[string]float64, error) {
	if s == "" {
		return nil, nil
	}
	var m map[string]float64
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	if len(m) == 0 {
		return nil, nil
	}
	return m, nil
}

// --- Remote agent updates (SPEC-v0.6 §2) ---

// CreateUpdateJob inserts j (ignoring j.ID) and returns the row with its
// assigned ID and CreatedAt/UpdatedAt populated.
func (db *DB) CreateUpdateJob(ctx context.Context, j models.UpdateJob) (models.UpdateJob, error) {
	now := time.Now().Unix()
	attempt := j.Attempt
	if attempt == 0 {
		attempt = 1
	}
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO update_jobs (batch_id, host_id, target, state, reason, error, created_at, updated_at, attempt)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, j.BatchID, j.HostID, j.Target, string(j.State), string(j.Reason), j.Error, now, now, attempt)
	if err != nil {
		return models.UpdateJob{}, fmt.Errorf("storage: create update job: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.UpdateJob{}, fmt.Errorf("storage: create update job: last insert id: %w", err)
	}
	j.ID = id
	j.CreatedAt, j.UpdatedAt = now, now
	j.Attempt = attempt
	return j, nil
}

// GetUpdateJob returns one job by id, or models.ErrNotFound.
func (db *DB) GetUpdateJob(ctx context.Context, id int64) (models.UpdateJob, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, batch_id, host_id, target, state, reason, error, created_at, updated_at, attempt
		FROM update_jobs WHERE id = ?
	`, id)
	j, err := scanUpdateJob(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.UpdateJob{}, fmt.Errorf("storage: get update job: %w", models.ErrNotFound)
		}
		return models.UpdateJob{}, fmt.Errorf("storage: get update job: %w", err)
	}
	return j, nil
}

// UpdateUpdateJob replaces the stored job matching j.ID with j
// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no job
// with that ID exists.
func (db *DB) UpdateUpdateJob(ctx context.Context, j models.UpdateJob) error {
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE update_jobs SET
			batch_id = ?, host_id = ?, target = ?, state = ?, reason = ?, error = ?, updated_at = ?, attempt = ?
		WHERE id = ?
	`, j.BatchID, j.HostID, j.Target, string(j.State), string(j.Reason), j.Error, now, j.Attempt, j.ID)
	if err != nil {
		return fmt.Errorf("storage: update update job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("storage: update update job: rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("storage: update update job: %w", models.ErrNotFound)
	}
	return nil
}

// ListUpdateJobs returns jobs matching the given filters, newest first.
// batchID == "" matches any batch; state == "" matches any state.
func (db *DB) ListUpdateJobs(ctx context.Context, batchID string, state models.UpdateJobState) ([]models.UpdateJob, error) {
	query := `
		SELECT id, batch_id, host_id, target, state, reason, error, created_at, updated_at, attempt
		FROM update_jobs WHERE 1=1
	`
	args := make([]any, 0, 2)
	if batchID != "" {
		query += ` AND batch_id = ?`
		args = append(args, batchID)
	}
	if state != "" {
		query += ` AND state = ?`
		args = append(args, string(state))
	}
	query += ` ORDER BY created_at DESC, id DESC`

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list update jobs: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.UpdateJob, 0)
	for rows.Next() {
		j, err := scanUpdateJob(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list update jobs: %w", err)
		}
		out = append(out, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list update jobs: %w", err)
	}
	return out, nil
}

// GetLatestUpdateJobForHost returns the most recently created job for
// hostID (any state), or models.ErrNotFound if none exists.
func (db *DB) GetLatestUpdateJobForHost(ctx context.Context, hostID string) (models.UpdateJob, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, batch_id, host_id, target, state, reason, error, created_at, updated_at, attempt
		FROM update_jobs WHERE host_id = ?
		ORDER BY created_at DESC, id DESC LIMIT 1
	`, hostID)
	j, err := scanUpdateJob(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.UpdateJob{}, fmt.Errorf("storage: get latest update job for host: %w", models.ErrNotFound)
		}
		return models.UpdateJob{}, fmt.Errorf("storage: get latest update job for host: %w", err)
	}
	return j, nil
}

func scanUpdateJob(scan func(dest ...any) error) (models.UpdateJob, error) {
	var j models.UpdateJob
	var state, reason string
	if err := scan(&j.ID, &j.BatchID, &j.HostID, &j.Target, &state, &reason, &j.Error,
		&j.CreatedAt, &j.UpdatedAt, &j.Attempt); err != nil {
		return models.UpdateJob{}, err
	}
	j.State = models.UpdateJobState(state)
	j.Reason = models.UpdateJobReason(reason)
	return j, nil
}

// --- Network cost estimation (SPEC-v0.6 §3) ---

// ListPricingPlans returns every configured pricing plan, sorted by ID.
func (db *DB) ListPricingPlans(ctx context.Context) ([]models.PricingPlan, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, name, provider, currency, egress_free_gb, egress_tiers,
		       ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at
		FROM pricing_plans ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list pricing plans: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.PricingPlan, 0)
	for rows.Next() {
		p, err := scanPricingPlan(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list pricing plans: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list pricing plans: %w", err)
	}
	return out, nil
}

// GetPricingPlan returns one plan by id, or models.ErrNotFound.
func (db *DB) GetPricingPlan(ctx context.Context, id int64) (models.PricingPlan, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, name, provider, currency, egress_free_gb, egress_tiers,
		       ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at
		FROM pricing_plans WHERE id = ?
	`, id)
	p, err := scanPricingPlan(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.PricingPlan{}, fmt.Errorf("storage: get pricing plan: %w", models.ErrNotFound)
		}
		return models.PricingPlan{}, fmt.Errorf("storage: get pricing plan: %w", err)
	}
	return p, nil
}

// CreatePricingPlan inserts p (ignoring p.ID; p.Builtin is always
// stored false for a newly created plan) and returns the row with its
// assigned ID and CreatedAt/UpdatedAt populated.
func (db *DB) CreatePricingPlan(ctx context.Context, p models.PricingPlan) (models.PricingPlan, error) {
	tiersJSON, err := marshalPricingTiers(p.EgressTiers)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: create pricing plan: %w", err)
	}
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO pricing_plans (
			name, provider, currency, egress_free_gb, egress_tiers,
			ingress_price_per_gb, pool_free_tier, builtin, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
	`, p.Name, p.Provider, p.Currency, p.EgressFreeGB, tiersJSON, p.IngressPricePerGB, p.PoolFreeTier, now, now)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: create pricing plan: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: create pricing plan: last insert id: %w", err)
	}
	p.ID = id
	p.Builtin = false
	p.CreatedAt, p.UpdatedAt = now, now
	return p, nil
}

// UpdatePricingPlan replaces the stored plan matching p.ID with p
// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no plan
// with that ID exists. Callers must reject an attempt to update a
// Builtin plan before calling this method.
func (db *DB) UpdatePricingPlan(ctx context.Context, p models.PricingPlan) (models.PricingPlan, error) {
	tiersJSON, err := marshalPricingTiers(p.EgressTiers)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: update pricing plan: %w", err)
	}
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE pricing_plans SET
			name = ?, provider = ?, currency = ?, egress_free_gb = ?, egress_tiers = ?,
			ingress_price_per_gb = ?, pool_free_tier = ?, updated_at = ?
		WHERE id = ?
	`, p.Name, p.Provider, p.Currency, p.EgressFreeGB, tiersJSON, p.IngressPricePerGB, p.PoolFreeTier, now, p.ID)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: update pricing plan: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: update pricing plan: rows affected: %w", err)
	}
	if n == 0 {
		return models.PricingPlan{}, fmt.Errorf("storage: update pricing plan: %w", models.ErrNotFound)
	}
	updated, err := db.GetPricingPlan(ctx, p.ID)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("storage: update pricing plan: reload: %w", err)
	}
	return updated, nil
}

// DeletePricingPlan removes the plan with id. Deleting a non-existent
// plan is not an error. Callers must reject an attempt to delete a
// Builtin plan before calling this method.
func (db *DB) DeletePricingPlan(ctx context.Context, id int64) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM pricing_plans WHERE id = ?`, id); err != nil {
		return fmt.Errorf("storage: delete pricing plan: %w", err)
	}
	return nil
}

func scanPricingPlan(scan func(dest ...any) error) (models.PricingPlan, error) {
	var p models.PricingPlan
	var tiersJSON string
	if err := scan(&p.ID, &p.Name, &p.Provider, &p.Currency, &p.EgressFreeGB, &tiersJSON,
		&p.IngressPricePerGB, &p.PoolFreeTier, &p.Builtin, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return models.PricingPlan{}, err
	}
	tiers, err := unmarshalPricingTiers(tiersJSON)
	if err != nil {
		return models.PricingPlan{}, fmt.Errorf("unmarshal egress_tiers: %w", err)
	}
	p.EgressTiers = tiers
	return p, nil
}

func marshalPricingTiers(tiers []models.PricingTier) (string, error) {
	if tiers == nil {
		tiers = []models.PricingTier{}
	}
	b, err := json.Marshal(tiers)
	if err != nil {
		return "", fmt.Errorf("marshal egress_tiers: %w", err)
	}
	return string(b), nil
}

func unmarshalPricingTiers(s string) ([]models.PricingTier, error) {
	if s == "" {
		return []models.PricingTier{}, nil
	}
	var tiers []models.PricingTier
	if err := json.Unmarshal([]byte(s), &tiers); err != nil {
		return nil, err
	}
	if tiers == nil {
		tiers = []models.PricingTier{}
	}
	return tiers, nil
}

// GetHostPricing returns the pricing plan assignment for hostID, or a
// zero-value models.HostPricing{HostID: hostID, PlanID: 0} with a nil
// error if no row exists.
func (db *DB) GetHostPricing(ctx context.Context, hostID string) (models.HostPricing, error) {
	row := db.sql.QueryRowContext(ctx, `SELECT host_id, plan_id FROM host_pricing WHERE host_id = ?`, hostID)
	var hp models.HostPricing
	if err := row.Scan(&hp.HostID, &hp.PlanID); err != nil {
		if isNoRows(err) {
			return models.HostPricing{HostID: hostID}, nil
		}
		return models.HostPricing{}, fmt.Errorf("storage: get host pricing: %w", err)
	}
	return hp, nil
}

// ListHostPricing returns every host's stored plan assignment, sorted by
// host ID.
func (db *DB) ListHostPricing(ctx context.Context) ([]models.HostPricing, error) {
	rows, err := db.sql.QueryContext(ctx, `SELECT host_id, plan_id FROM host_pricing ORDER BY host_id`)
	if err != nil {
		return nil, fmt.Errorf("storage: list host pricing: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.HostPricing, 0)
	for rows.Next() {
		var hp models.HostPricing
		if err := rows.Scan(&hp.HostID, &hp.PlanID); err != nil {
			return nil, fmt.Errorf("storage: list host pricing: %w", err)
		}
		out = append(out, hp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list host pricing: %w", err)
	}
	return out, nil
}

// SetHostPricing upserts hp's assignment for hp.HostID.
func (db *DB) SetHostPricing(ctx context.Context, hp models.HostPricing) error {
	now := time.Now().Unix()
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO host_pricing (host_id, plan_id, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(host_id) DO UPDATE SET plan_id = excluded.plan_id, updated_at = excluded.updated_at
	`, hp.HostID, hp.PlanID, now)
	if err != nil {
		return fmt.Errorf("storage: set host pricing: %w", err)
	}
	return nil
}

// --- Audit log (SPEC-v0.6 §3 개선 c) ---

// CreateAuditEntry inserts e (ignoring e.ID) and returns the row with
// its assigned ID populated.
func (db *DB) CreateAuditEntry(ctx context.Context, e models.AuditEntry) (models.AuditEntry, error) {
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO audit_log (at, actor, remote, action, entity_type, entity_id, before_json, after_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, e.At, e.Actor, e.Remote, string(e.Action), e.EntityType, e.EntityID, e.BeforeJSON, e.AfterJSON)
	if err != nil {
		return models.AuditEntry{}, fmt.Errorf("storage: create audit entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.AuditEntry{}, fmt.Errorf("storage: create audit entry: last insert id: %w", err)
	}
	e.ID = id
	return e, nil
}

// ListAuditEntries returns entries matching the given filters, newest
// first, at most limit rows. entityType == "" matches any entity type;
// before == 0 means no upper bound on At.
func (db *DB) ListAuditEntries(ctx context.Context, entityType string, before int64, limit int) ([]models.AuditEntry, error) {
	query := `
		SELECT id, at, actor, remote, action, entity_type, entity_id, before_json, after_json
		FROM audit_log WHERE 1=1
	`
	args := make([]any, 0, 3)
	if entityType != "" {
		query += ` AND entity_type = ?`
		args = append(args, entityType)
	}
	if before != 0 {
		query += ` AND at < ?`
		args = append(args, before)
	}
	query += ` ORDER BY at DESC, id DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}

	rows, err := db.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("storage: list audit entries: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.AuditEntry, 0)
	for rows.Next() {
		var e models.AuditEntry
		var action string
		if err := rows.Scan(&e.ID, &e.At, &e.Actor, &e.Remote, &action, &e.EntityType, &e.EntityID, &e.BeforeJSON, &e.AfterJSON); err != nil {
			return nil, fmt.Errorf("storage: list audit entries: %w", err)
		}
		e.Action = models.AuditAction(action)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list audit entries: %w", err)
	}
	return out, nil
}

// PruneAuditEntries deletes entries older than models.AuditRetentionDays
// as of now.
func (db *DB) PruneAuditEntries(ctx context.Context, now time.Time) error {
	cutoff := now.AddDate(0, 0, -models.AuditRetentionDays).Unix()
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM audit_log WHERE at < ?`, cutoff); err != nil {
		return fmt.Errorf("storage: prune audit entries: %w", err)
	}
	return nil
}
