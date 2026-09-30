// storageusage.go implements SPEC-v0.7 §3's storage_usage_pct alert
// metric: an account-scoped metric (not host-scoped) evaluated against
// every connected storage account's latest snapshot. See
// models.AlertMetricStorageUsagePct's doc comment for why this reuses
// AlertRule.HostID as a generic "scope ID" holding a storage account's
// decimal ID, and models.HostSnapshot for the synthetic per-account
// snapshot this file builds to drive the same state-machine transition
// functions (transitionToOK/Pending/Firing in state.go) that every
// other metric already uses — no duplicated state-machine logic.
package alerting

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// StorageAccountUsage is the per-account input EvaluateStorage
// evaluates rules against — the fields notifyEvent/buildMessage need
// from a storage account's latest snapshot, kept separate from
// models.StorageAccountSnapshot so this package has no dependency on
// internal/storageusage or a wider Store surface than it already
// declares.
type StorageAccountUsage struct {
	AccountID   int64
	AccountName string
	UsedPercent float64
	// Unlimited accounts (no quota ceiling) never satisfy a
	// storage_usage_pct rule — there is no percentage to compare
	// against a threshold.
	Unlimited bool
}

// EvaluateStorage runs every enabled storage_usage_pct rule against
// accounts as of now, advancing each rule+account's persisted state
// machine (the same alert_state rows every other metric uses, keyed by
// the account's ID formatted as a string in place of a host ID) and
// enqueuing any resulting notifications. Mirrors Evaluate's shape and
// error-handling convention (best-effort per rule+account, first error
// returned after processing every one).
func (e *Engine) EvaluateStorage(ctx context.Context, now time.Time, accounts []StorageAccountUsage) error {
	rules, err := e.store.ListAlertRules(ctx)
	if err != nil {
		return fmt.Errorf("alerting: evaluate storage: list rules: %w", err)
	}

	var firstErr error
	for _, rule := range rules {
		if !rule.Enabled || rule.Metric != models.AlertMetricStorageUsagePct {
			continue
		}
		for _, acct := range scopedStorageAccounts(rule, accounts) {
			if err := e.evaluateStorageRuleAccount(ctx, now, rule, acct); err != nil {
				e.logger.Error("alerting: evaluate storage rule failed",
					"rule_id", rule.ID, "account_id", acct.AccountID, "error", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

// scopedStorageAccounts filters accounts to those rule applies to:
// every account when rule.HostID == "", or just the matching one
// otherwise (rule.HostID holds a decimal account ID for this metric —
// see models.AlertMetricStorageUsagePct's doc comment). Sorted by
// AccountID for deterministic iteration order.
func scopedStorageAccounts(rule models.AlertRule, accounts []StorageAccountUsage) []StorageAccountUsage {
	var out []StorageAccountUsage
	if rule.HostID == "" {
		out = append(out, accounts...)
	} else {
		wantID, err := strconv.ParseInt(rule.HostID, 10, 64)
		if err != nil {
			return nil
		}
		for _, a := range accounts {
			if a.AccountID == wantID {
				out = append(out, a)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AccountID < out[j].AccountID })
	return out
}

// storageScopeID formats acct's ID the same way scopedStorageAccounts
// parses it back, and the same way a rule's HostID must be set to
// scope to one account.
func storageScopeID(accountID int64) string {
	return strconv.FormatInt(accountID, 10)
}

// evaluateStorageRuleAccount advances rule+account's persisted state
// machine by one step given the current condition. storage_usage_pct
// has no sustained-duration concept per SPEC-v0.7 §3 ("지속 시간
// 미사용") — DurationSec is ignored for this metric, so a breach fires
// immediately, matching duration_sec == 0's existing semantics for
// every other metric rather than introducing a third code path.
func (e *Engine) evaluateStorageRuleAccount(ctx context.Context, now time.Time, rule models.AlertRule, acct StorageAccountUsage) error {
	if acct.Unlimited {
		return nil
	}

	host := storageAccountAsHostSnapshot(acct)
	satisfiedNow := conditionSatisfiedNow(rule, acct.UsedPercent)

	st, err := e.store.GetAlertState(ctx, rule.ID, host.HostID)
	if err != nil {
		return fmt.Errorf("get alert state: %w", err)
	}

	if !satisfiedNow {
		return e.transitionToOK(ctx, now, rule, host, st, acct.UsedPercent)
	}
	// SPEC-v0.7 §3: "하루 1회 재알림 기본" (default daily re-notify) —
	// applied by the API layer defaulting a new storage_usage_pct
	// rule's CooldownSec to 86400 rather than the general
	// alertingDefaultCooldownSec (3600), matching how egress rules get
	// their own default treatment at creation time; the state machine
	// itself just uses whatever CooldownSec the rule carries, same as
	// every other metric via transitionToFiring/maybeRenotifyStillFiring.
	return e.transitionToFiring(ctx, now, rule, host, st, acct.UsedPercent)
}

// storageAccountAsHostSnapshot adapts acct into the models.HostSnapshot
// shape transitionToOK/transitionToFiring/notifyEvent already know how
// to consume — HostID carries the account's scope-ID string,
// Hostname carries its display name for notification titles/fields.
// Latest/Status/LastSeen/Egress are left zero-valued; nothing in the
// storage_usage_pct path reads them (no sustained-window/host_down/
// egress logic applies to this metric).
func storageAccountAsHostSnapshot(acct StorageAccountUsage) models.HostSnapshot {
	name := acct.AccountName
	if name == "" {
		name = storageScopeID(acct.AccountID)
	}
	return models.HostSnapshot{
		HostID:   storageScopeID(acct.AccountID),
		Hostname: name,
	}
}
