package hub

import (
	"context"
	"fmt"
)

// EnsureDefaultCredentials bootstraps the single admin account on a
// fresh hub: when no password hash is stored yet, it stores a hash of
// the default password "changeme" and sets the must-change-password
// flag. cmd/hub calls this once before serving; a non-nil error is
// fatal (logged, process exits 1) since the hub cannot safely serve the
// dashboard without a resolvable admin credential state.
//
// On every startup (not just bootstrap), while the must-change flag is
// set, it logs a warning reminding the operator to sign in and change
// the password — escalated to an error-level warning when the hub is
// also configured with an allow-all CIDR allowlist (nil
// AllowedCIDRs), since that combination means the default password is
// reachable from anywhere the hub is exposed.
func (s *Server) EnsureDefaultCredentials(ctx context.Context) error {
	_, ok, err := s.store.GetSetting(ctx, SettingPasswordHash)
	if err != nil {
		return fmt.Errorf("hub: check password hash setting: %w", err)
	}
	if !ok {
		hash, err := hashPassword(defaultPassword)
		if err != nil {
			return fmt.Errorf("hub: hash default password: %w", err)
		}
		if err := s.store.SetSetting(ctx, SettingPasswordHash, hash); err != nil {
			return fmt.Errorf("hub: store default password hash: %w", err)
		}
		if err := s.store.SetSetting(ctx, SettingMustChangePassword, "1"); err != nil {
			return fmt.Errorf("hub: store must-change-password flag: %w", err)
		}
		s.logger.Warn("bootstrapped default dashboard credentials",
			"username", adminUsername,
			"note", "default password in use; sign in and change it",
		)
		s.warnIfDefaultPasswordExposed(ctx)
		return nil
	}

	if s.accountMustChangePassword(ctx) {
		s.logger.Warn("default dashboard password in use; sign in and change it")
		s.warnIfDefaultPasswordExposed(ctx)
	}
	return nil
}

// warnIfDefaultPasswordExposed logs an error-level warning when the
// must-change-password flag is set AND the hub's CIDR allowlist allows
// every address, since that combination means the default password
// "changeme" is reachable from anywhere the hub is exposed on the
// network.
func (s *Server) warnIfDefaultPasswordExposed(_ context.Context) {
	if s.opts.AllowedCIDRs == nil {
		s.logger.Error("SECURITY: default dashboard password is in use AND CP_ALLOWED_CIDRS allows all addresses; " +
			"the hub is reachable from anywhere it is exposed with a well-known default password")
	}
}
