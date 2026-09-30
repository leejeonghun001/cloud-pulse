// updateunit.go renders the two additional systemd units SPEC-v0.6 §2
// introduces for the remote agent-update feature:
// cloud-pulse-agent-update.path (watches the unprivileged agent's
// request file) and cloud-pulse-agent-update.service (the root oneshot
// that actually runs `cloud-pulse-agent update --from-request`). Both
// are only ever installed/enabled when the agent has opted in
// (CP_REMOTE_UPDATE=on) — see install-agent.sh's --remote-update flag
// and ApplyUpdateUnits below.
package systemdunit

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// UpdatePathUnitName and UpdateServiceUnitName are the two additional
// unit file basenames this file renders.
const (
	UpdatePathUnitName    = "cloud-pulse-agent-update.path"
	UpdateServiceUnitName = "cloud-pulse-agent-update.service"
)

// UpdateUnitParams configures RenderUpdatePath/RenderUpdateService.
type UpdateUnitParams struct {
	// BinPath is the absolute path to the installed cloud-pulse-agent
	// binary, e.g. "/usr/local/bin/cloud-pulse-agent". Required.
	BinPath string
	// RequestPath is the absolute path to the request file the .path
	// unit watches, e.g.
	// "/var/lib/cloud-pulse-agent/update-request.json". Required.
	RequestPath string
	// ResultDir is the absolute path to the root-owned result
	// directory `update --from-request` writes into, e.g.
	// "/var/lib/cloud-pulse-agent-update". Required.
	ResultDir string
}

func validateUpdateUnitParams(p UpdateUnitParams) error {
	if p.BinPath == "" {
		return errors.New("bin-path is required")
	}
	if p.RequestPath == "" {
		return errors.New("request-path is required")
	}
	if p.ResultDir == "" {
		return errors.New("result-dir is required")
	}
	if err := validatePathValue("bin-path", p.BinPath); err != nil {
		return err
	}
	if err := validatePathValue("request-path", p.RequestPath); err != nil {
		return err
	}
	if err := validatePathValue("result-dir", p.ResultDir); err != nil {
		return err
	}
	return nil
}

// RenderUpdatePath renders cloud-pulse-agent-update.path: a systemd
// path unit that watches PathModified=RequestPath and triggers
// cloud-pulse-agent-update.service whenever the unprivileged agent
// process (re)writes its request file (SPEC-v0.6 §2 step 4).
// PathExists is also set so a request file already present when the
// path unit starts (e.g. right after a host reboot) is picked up
// immediately rather than waiting for a subsequent write.
func RenderUpdatePath(p UpdateUnitParams) (string, error) {
	if err := validateUpdateUnitParams(p); err != nil {
		return "", fmt.Errorf("systemdunit: render update path unit: %w", err)
	}
	var b strings.Builder
	writeLine(&b, "[Unit]")
	writeLine(&b, "Description=Watch for cloud-pulse-agent remote update requests")
	writeLine(&b, "")
	writeLine(&b, "[Path]")
	writeLine(&b, "PathModified="+p.RequestPath)
	writeLine(&b, "PathExists="+p.RequestPath)
	writeLine(&b, "Unit="+UpdateServiceUnitName)
	writeLine(&b, "")
	writeLine(&b, "[Install]")
	writeLine(&b, "WantedBy=multi-user.target")
	return b.String(), nil
}

// RenderUpdateService renders cloud-pulse-agent-update.service: a root
// oneshot unit invoking `cloud-pulse-agent update --from-request
// <RequestPath> --result-dir <ResultDir>` (SPEC-v0.6 §2 step 4). It
// deliberately has no [Install] section — it is only ever started by
// the .path unit above, never enabled for boot on its own — and no
// User=/Group= override, since it must run as root to write
// ResultDir (a directory the unprivileged agent process cannot write
// to, closing the symlink-attack surface SPEC-v0.6 §2 calls out).
// RemainAfterExit is not set: each trigger is a fresh, independent
// run.
func RenderUpdateService(p UpdateUnitParams) (string, error) {
	if err := validateUpdateUnitParams(p); err != nil {
		return "", fmt.Errorf("systemdunit: render update service unit: %w", err)
	}
	var b strings.Builder
	writeLine(&b, "[Unit]")
	writeLine(&b, "Description=Apply a hub-requested cloud-pulse-agent update")
	writeLine(&b, "")
	writeLine(&b, "[Service]")
	writeLine(&b, "Type=oneshot")
	writeLine(&b, fmt.Sprintf("ExecStart=%s update --from-request %s --result-dir %s", p.BinPath, p.RequestPath, p.ResultDir))
	return b.String(), nil
}

// ApplyUpdateUnits creates or updates cloud-pulse-agent-update.path/
// .service at pathUnitPath/serviceUnitPath to match
// RenderUpdatePath/RenderUpdateService's current output. It never
// itself runs systemctl (enable/daemon-reload/start) — this package has
// no dependency beyond the standard library, and every other Apply-style
// function here follows the same split: callers (cmd/agent's
// `systemd-unit apply`, or install-agent.sh) enable the .path unit only
// when the agent has opted in (CP_REMOTE_UPDATE=on, SPEC-v0.6 §2: "옵트인
// 시에만 enable"); the .service unit is never itself enabled, only
// triggered by the .path unit.
//
// Returns whether either unit file's content actually changed (for the
// caller to decide whether a daemon-reload is warranted — mirroring
// Apply's own changed/unchanged reporting).
func ApplyUpdateUnits(pathUnitPath, serviceUnitPath string, p UpdateUnitParams, out interface{ Write([]byte) (int, error) }) (changed bool, err error) {
	pathUnit, err := RenderUpdatePath(p)
	if err != nil {
		return false, err
	}
	serviceUnit, err := RenderUpdateService(p)
	if err != nil {
		return false, err
	}

	pathChanged, err := writeUnitIfChanged(pathUnitPath, pathUnit, out)
	if err != nil {
		return false, fmt.Errorf("systemdunit: apply update units: %w", err)
	}
	serviceChanged, err := writeUnitIfChanged(serviceUnitPath, serviceUnit, out)
	if err != nil {
		return false, fmt.Errorf("systemdunit: apply update units: %w", err)
	}
	return pathChanged || serviceChanged, nil
}

// writeUnitIfChanged writes content to path only if the file is
// missing or its current content differs, via the same
// temp-file-then-rename-with-backup pattern Apply uses. Returns
// changed=false, nil error when the file already matches content
// exactly (including when it already exists with identical bytes).
func writeUnitIfChanged(path, content string, out interface{ Write([]byte) (int, error) }) (bool, error) {
	existing, err := os.ReadFile(path) //nolint:gosec // path is an operator-supplied systemd unit path, not user request input
	if err == nil && string(existing) == content {
		printfln(out, "systemd unit up to date: %s", path)
		return false, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("read %s: %w", path, err)
	}

	if err == nil {
		backupPath := path + ".bak"
		if err := os.WriteFile(backupPath, existing, 0o644); err != nil { //nolint:gosec // unit files are world-readable by design
			return false, fmt.Errorf("write backup %s: %w", backupPath, err)
		}
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(content), 0o644); err != nil { //nolint:gosec // unit files are world-readable by design
		return false, fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	if err == nil {
		printfln(out, "updated systemd unit: %s (previous content saved to %s.bak)", path, path)
	} else {
		printfln(out, "created systemd unit: %s", path)
	}
	return true, nil
}

// ParseUpdateUnitParams extracts UpdateUnitParams from an already
// rendered cloud-pulse-agent-update.path/.service pair's text, for a
// future drift-detection Apply pass (mirroring ParseExisting's role for
// the main hub/agent units). Only RequestPath (from the .path unit's
// PathModified=) and BinPath+ResultDir (parsed from the .service
// unit's ExecStart= line) are recovered; a malformed or hand-edited
// unit returns an error rather than a best-effort partial result, the
// same defensive stance ParseExisting takes.
func ParseUpdateUnitParams(pathUnitText, serviceUnitText string) (UpdateUnitParams, error) {
	var p UpdateUnitParams

	for _, rawLine := range strings.Split(pathUnitText, "\n") {
		line := strings.TrimSpace(rawLine)
		if strings.HasPrefix(line, "PathModified=") {
			p.RequestPath = strings.TrimSpace(strings.TrimPrefix(line, "PathModified="))
		}
	}
	if p.RequestPath == "" {
		return UpdateUnitParams{}, errors.New("systemdunit: parse update units: missing PathModified= in path unit")
	}

	for _, rawLine := range strings.Split(serviceUnitText, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "ExecStart=") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "ExecStart="))
		// Expected shape: <bin> update --from-request <path> --result-dir <dir>
		for i := 0; i < len(fields); i++ {
			switch fields[i] {
			case "--from-request":
				if i+1 < len(fields) {
					// Already captured via the .path unit above;
					// cross-check for consistency.
					if fields[i+1] != p.RequestPath {
						return UpdateUnitParams{}, fmt.Errorf("systemdunit: parse update units: service --from-request %q does not match path unit's PathModified= %q", fields[i+1], p.RequestPath)
					}
				}
			case "--result-dir":
				if i+1 < len(fields) {
					p.ResultDir = fields[i+1]
				}
			}
		}
		if len(fields) > 0 {
			p.BinPath = fields[0]
		}
	}

	if p.BinPath == "" || p.ResultDir == "" {
		return UpdateUnitParams{}, errors.New("systemdunit: parse update units: missing ExecStart= fields in service unit")
	}

	if err := validateUpdateUnitParams(p); err != nil {
		return UpdateUnitParams{}, fmt.Errorf("systemdunit: parse update units: %w", err)
	}
	return p, nil
}
