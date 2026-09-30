// v07_stub.go implements the v0.7.0 additions to hub.Store
// (storage-usage accounts + snapshots) against the schema in
// migrations/0006_v07.sql. Named "_stub" following v06_stub.go's
// convention: written once by the prep stage purely so storage.DB
// keeps satisfying hub.Store as the interface grows. The owning
// v0.7.0 "storage" stage may replace/extend these implementations, but
// must not change the Store interface's method signatures without
// updating server.go and every other stage's fakes in lockstep — see
// notes/v07-prep.md for exact per-stage file ownership.
package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// --- Storage usage accounts (SPEC-v0.7 §3) ---

// ListStorageAccounts returns every configured storage-usage account,
// sorted by id.
func (db *DB) ListStorageAccounts(ctx context.Context) ([]models.StorageAccount, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT id, provider, name, config_json, secret_json, created_at, updated_at
		FROM storage_accounts ORDER BY id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list storage accounts: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.StorageAccount, 0)
	for rows.Next() {
		a, err := scanStorageAccountRow(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("storage: list storage accounts: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list storage accounts: %w", err)
	}
	return out, nil
}

// GetStorageAccount returns one account by id, or models.ErrNotFound.
func (db *DB) GetStorageAccount(ctx context.Context, id int64) (models.StorageAccount, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT id, provider, name, config_json, secret_json, created_at, updated_at
		FROM storage_accounts WHERE id = ?
	`, id)
	a, err := scanStorageAccountRow(row.Scan)
	if err != nil {
		if isNoRows(err) {
			return models.StorageAccount{}, models.ErrNotFound
		}
		return models.StorageAccount{}, fmt.Errorf("storage: get storage account: %w", err)
	}
	return a, nil
}

// CreateStorageAccount inserts a, ignoring a.ID, and returns the row
// with its assigned ID and CreatedAt/UpdatedAt populated.
func (db *DB) CreateStorageAccount(ctx context.Context, a models.StorageAccount) (models.StorageAccount, error) {
	configJSON, err := marshalStringMap(a.Config)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: create storage account: %w", err)
	}
	secretJSON, err := marshalStringMap(a.Secret)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: create storage account: %w", err)
	}
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		INSERT INTO storage_accounts (provider, name, config_json, secret_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, string(a.Provider), a.Name, configJSON, secretJSON, now, now)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: create storage account: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: create storage account: last insert id: %w", err)
	}
	a.ID = id
	a.CreatedAt, a.UpdatedAt = now, now
	return a, nil
}

// UpdateStorageAccount replaces the stored account matching a.ID with a
// (UpdatedAt refreshed to now), or returns models.ErrNotFound if no
// account with that ID exists.
func (db *DB) UpdateStorageAccount(ctx context.Context, a models.StorageAccount) (models.StorageAccount, error) {
	configJSON, err := marshalStringMap(a.Config)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: update storage account: %w", err)
	}
	secretJSON, err := marshalStringMap(a.Secret)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: update storage account: %w", err)
	}
	now := time.Now().Unix()
	res, err := db.sql.ExecContext(ctx, `
		UPDATE storage_accounts
		SET provider = ?, name = ?, config_json = ?, secret_json = ?, updated_at = ?
		WHERE id = ?
	`, string(a.Provider), a.Name, configJSON, secretJSON, now, a.ID)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: update storage account: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("storage: update storage account: rows affected: %w", err)
	}
	if affected == 0 {
		return models.StorageAccount{}, models.ErrNotFound
	}
	a.UpdatedAt = now
	return a, nil
}

// DeleteStorageAccount removes the account with id (and, via the
// schema's ON DELETE CASCADE, its snapshot row). Deleting a
// non-existent account is not an error.
func (db *DB) DeleteStorageAccount(ctx context.Context, id int64) error {
	if _, err := db.sql.ExecContext(ctx, `DELETE FROM storage_accounts WHERE id = ?`, id); err != nil {
		return fmt.Errorf("storage: delete storage account: %w", err)
	}
	return nil
}

func scanStorageAccountRow(scan func(dest ...any) error) (models.StorageAccount, error) {
	var a models.StorageAccount
	var provider, configJSON, secretJSON string
	if err := scan(&a.ID, &provider, &a.Name, &configJSON, &secretJSON, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return models.StorageAccount{}, err
	}
	a.Provider = models.StorageAccountProvider(provider)
	config, err := unmarshalStringMap(configJSON)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("unmarshal config_json: %w", err)
	}
	a.Config = config
	secret, err := unmarshalStringMap(secretJSON)
	if err != nil {
		return models.StorageAccount{}, fmt.Errorf("unmarshal secret_json: %w", err)
	}
	a.Secret = secret
	return a, nil
}

func marshalStringMap(m map[string]string) (string, error) {
	if m == nil {
		m = map[string]string{}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal string map: %w", err)
	}
	return string(b), nil
}

func unmarshalStringMap(s string) (map[string]string, error) {
	if s == "" || s == "{}" {
		return nil, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return nil, err
	}
	return m, nil
}

// GetStorageAccountSnapshot returns the persisted snapshot for
// accountID, or a zero-value models.StorageAccountSnapshot{AccountID:
// accountID, Status: models.StorageAccountNotConfigured} with a nil
// error if no row exists yet.
func (db *DB) GetStorageAccountSnapshot(ctx context.Context, accountID int64) (models.StorageAccountSnapshot, error) {
	row := db.sql.QueryRowContext(ctx, `
		SELECT status, status_detail, account_email, account_name, used_bytes, limit_bytes,
		       unlimited, trash_bytes, allocation_type, collected_at, last_success_at, last_attempt_at
		FROM storage_snapshots WHERE account_id = ?
	`, accountID)
	snap, err := scanStorageSnapshotRow(row.Scan, accountID)
	if err != nil {
		if isNoRows(err) {
			return models.StorageAccountSnapshot{AccountID: accountID, Status: models.StorageAccountNotConfigured}, nil
		}
		return models.StorageAccountSnapshot{}, fmt.Errorf("storage: get storage account snapshot: %w", err)
	}
	return snap, nil
}

// ListStorageAccountSnapshots returns every persisted account snapshot,
// sorted by account_id.
func (db *DB) ListStorageAccountSnapshots(ctx context.Context) ([]models.StorageAccountSnapshot, error) {
	rows, err := db.sql.QueryContext(ctx, `
		SELECT account_id, status, status_detail, account_email, account_name, used_bytes, limit_bytes,
		       unlimited, trash_bytes, allocation_type, collected_at, last_success_at, last_attempt_at
		FROM storage_snapshots ORDER BY account_id
	`)
	if err != nil {
		return nil, fmt.Errorf("storage: list storage account snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }() // best-effort close; loop below owns the returned error

	out := make([]models.StorageAccountSnapshot, 0)
	for rows.Next() {
		var accountID int64
		var status string
		var snap models.StorageAccountSnapshot
		var unlimitedInt int
		if err := rows.Scan(&accountID, &status, &snap.StatusDetail, &snap.AccountEmail, &snap.AccountName,
			&snap.Quota.UsedBytes, &snap.Quota.LimitBytes, &unlimitedInt, &snap.Quota.TrashBytes,
			&snap.Quota.AllocationType, &snap.CollectedAt, &snap.LastSuccessAt, &snap.LastAttemptAt); err != nil {
			return nil, fmt.Errorf("storage: list storage account snapshots: %w", err)
		}
		snap.AccountID = accountID
		snap.Status = models.StorageAccountStatus(status)
		snap.Quota.Unlimited = unlimitedInt != 0
		out = append(out, snap)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("storage: list storage account snapshots: %w", err)
	}
	return out, nil
}

// SetStorageAccountSnapshot upserts the snapshot for snap.AccountID.
func (db *DB) SetStorageAccountSnapshot(ctx context.Context, snap models.StorageAccountSnapshot) error {
	unlimitedInt := 0
	if snap.Quota.Unlimited {
		unlimitedInt = 1
	}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO storage_snapshots (
			account_id, status, status_detail, account_email, account_name, used_bytes, limit_bytes,
			unlimited, trash_bytes, allocation_type, collected_at, last_success_at, last_attempt_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id) DO UPDATE SET
			status = excluded.status,
			status_detail = excluded.status_detail,
			account_email = excluded.account_email,
			account_name = excluded.account_name,
			used_bytes = excluded.used_bytes,
			limit_bytes = excluded.limit_bytes,
			unlimited = excluded.unlimited,
			trash_bytes = excluded.trash_bytes,
			allocation_type = excluded.allocation_type,
			collected_at = excluded.collected_at,
			last_success_at = excluded.last_success_at,
			last_attempt_at = excluded.last_attempt_at
	`, snap.AccountID, string(snap.Status), snap.StatusDetail, snap.AccountEmail, snap.AccountName,
		snap.Quota.UsedBytes, snap.Quota.LimitBytes, unlimitedInt, snap.Quota.TrashBytes,
		snap.Quota.AllocationType, snap.CollectedAt, snap.LastSuccessAt, snap.LastAttemptAt)
	if err != nil {
		return fmt.Errorf("storage: set storage account snapshot: %w", err)
	}
	return nil
}

func scanStorageSnapshotRow(scan func(dest ...any) error, accountID int64) (models.StorageAccountSnapshot, error) {
	var snap models.StorageAccountSnapshot
	var status string
	var unlimitedInt int
	if err := scan(&status, &snap.StatusDetail, &snap.AccountEmail, &snap.AccountName,
		&snap.Quota.UsedBytes, &snap.Quota.LimitBytes, &unlimitedInt, &snap.Quota.TrashBytes,
		&snap.Quota.AllocationType, &snap.CollectedAt, &snap.LastSuccessAt, &snap.LastAttemptAt); err != nil {
		return models.StorageAccountSnapshot{}, err
	}
	snap.AccountID = accountID
	snap.Status = models.StorageAccountStatus(status)
	snap.Quota.Unlimited = unlimitedInt != 0
	return snap, nil
}
