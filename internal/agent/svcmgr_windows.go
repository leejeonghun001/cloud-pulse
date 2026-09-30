//go:build windows

package agent

import (
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// WindowsServiceDisplayName is the human-readable service name shown
// in services.msc / `sc query`.
const WindowsServiceDisplayName = "cloud-pulse-agent"

// WindowsServiceDescription is the service description SPEC-v0.7 §1
// implies via its "virtual account" / recovery-policy requirements.
const WindowsServiceDescription = "cloud-pulse host metrics collector (see https://github.com/leejeonghun001/cloud-pulse)"

// windowsServiceAccount is the fixed virtual account the agent service
// runs as (SPEC-v0.7 §1: "NT SERVICE\cloud-pulse-agent"), a
// per-service virtual account Windows creates automatically — no
// password, no manual account provisioning, and the SCM grants it a
// unique, unforgeable SID scoped to exactly this service (see Microsoft's
// "Service Accounts" documentation on Virtual Accounts). Analogous to
// systemd's DynamicUser=/a dedicated system user on Linux/macOS.
const windowsServiceAccount = `NT SERVICE\` + WindowsServiceName

// recoveryActionDelay is SPEC-v0.7 §1's fixed restart-on-failure delay
// ("실패 시 5초 후 재시작").
const recoveryActionDelay = 5 * time.Second

// recoveryResetPeriodSeconds resets the SCM's failure counter after an
// hour with no further failures, a conventional default that avoids an
// old, long-resolved failure streak counting against a service that's
// otherwise been healthy for a long time.
const recoveryResetPeriodSeconds = 3600

// InstallWindowsService registers the cloud-pulse-agent Windows
// service: binaryPath is the full command line (exe path plus any
// fixed arguments, e.g. "--env-file C:\ProgramData\...\agent.env"),
// run under the NT SERVICE virtual account, auto-start, with a
// restart-on-failure recovery action after recoveryActionDelay. Returns
// an error if a service by this name is already registered (the
// caller — `service install` — is responsible for deciding whether
// that's a hard error or an "already installed" no-op).
func InstallWindowsService(binaryPath string) error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("agent: install windows service: connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.CreateService(WindowsServiceName, binaryPath, mgr.Config{
		ServiceType:      windowsServiceTypeOwnProcess(),
		StartType:        windowsServiceStartAuto(),
		DisplayName:      WindowsServiceDisplayName,
		Description:      WindowsServiceDescription,
		ServiceStartName: windowsServiceAccount,
	})
	if err != nil {
		return fmt.Errorf("agent: install windows service: create service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: recoveryActionDelay},
	}, recoveryResetPeriodSeconds); err != nil {
		// Non-fatal: the service is already created and will run; a
		// missing recovery policy just means Windows won't
		// auto-restart it on crash. Reported to the caller as a
		// wrapped error so `service install`'s own exit code/message
		// still reflects it, but the service registration itself is
		// not rolled back.
		return fmt.Errorf("agent: install windows service: set recovery actions (service created, but auto-restart-on-failure was not configured): %w", err)
	}
	return nil
}

// UninstallWindowsService stops (if running) and deletes the
// cloud-pulse-agent service registration.
func UninstallWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("agent: uninstall windows service: connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(WindowsServiceName)
	if err != nil {
		return fmt.Errorf("agent: uninstall windows service: open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	_, _ = s.Control(svc.Stop) // best-effort; Delete works even if the stop attempt itself fails or the service was already stopped
	if err := s.Delete(); err != nil {
		return fmt.Errorf("agent: uninstall windows service: delete: %w", err)
	}
	return nil
}

// StartWindowsService starts an already-installed cloud-pulse-agent
// service.
func StartWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("agent: start windows service: connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(WindowsServiceName)
	if err != nil {
		return fmt.Errorf("agent: start windows service: open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.Start(); err != nil {
		return fmt.Errorf("agent: start windows service: %w", err)
	}
	return nil
}

// StopWindowsService requests the cloud-pulse-agent service stop and
// waits (best-effort, no long-poll here — the caller can call
// WindowsServiceStatus itself if it needs to poll for Stopped) for the
// initial control call to be accepted.
func StopWindowsService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("agent: stop windows service: connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(WindowsServiceName)
	if err != nil {
		return fmt.Errorf("agent: stop windows service: open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	if _, err := s.Control(svc.Stop); err != nil {
		return fmt.Errorf("agent: stop windows service: %w", err)
	}
	return nil
}

// SimpleServiceStatus is a minimal, build-tag-free view of a Windows
// service's status, returned by WindowsServiceStatus so callers in
// cmd/agent (compiled on every OS) never need to import
// golang.org/x/sys/windows/svc directly.
type SimpleServiceStatus struct {
	// State is the raw svc.State value (e.g. svc.Running == 4), kept
	// numeric here rather than re-exporting svc.State's named type so
	// this struct has zero Windows-only type dependencies.
	State uint32
	// ProcessID is the OS process ID of the running service, 0 if not
	// running.
	ProcessID uint32
}

// WindowsServiceStatus returns the current status of the
// cloud-pulse-agent service.
func WindowsServiceStatus() (SimpleServiceStatus, error) {
	m, err := mgr.Connect()
	if err != nil {
		return SimpleServiceStatus{}, fmt.Errorf("agent: windows service status: connect to service manager: %w", err)
	}
	defer func() { _ = m.Disconnect() }()

	s, err := m.OpenService(WindowsServiceName)
	if err != nil {
		return SimpleServiceStatus{}, fmt.Errorf("agent: windows service status: open service: %w", err)
	}
	defer func() { _ = s.Close() }()

	status, err := s.Query()
	if err != nil {
		return SimpleServiceStatus{}, fmt.Errorf("agent: windows service status: query: %w", err)
	}
	return SimpleServiceStatus{State: uint32(status.State), ProcessID: status.ProcessId}, nil
}

// windowsServiceTypeOwnProcess/windowsServiceStartAuto are tiny
// indirections so this file's mgr.Config literal reads as intent
// ("own process," "automatic start") rather than magic numbers,
// without importing golang.org/x/sys/windows directly just for two
// constants mgr already re-exports under different names in some
// versions — using the windows package's own constants directly here
// for clarity and to avoid a second import of mgr's internals.
func windowsServiceTypeOwnProcess() uint32 { return 0x00000010 } // SERVICE_WIN32_OWN_PROCESS
func windowsServiceStartAuto() uint32      { return 0x00000002 } // SERVICE_AUTO_START
