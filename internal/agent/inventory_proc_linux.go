//go:build linux

package agent

import (
	"os"
	"strconv"
	"strings"
)

// processNameForPID resolves pid's process name by reading
// /proc/<pid>/comm directly, avoiding a dependency on gopsutil's
// process subpackage for this single field. Returns "" on any failure
// (process exited, permission denied — the agent runs unprivileged and
// cannot always read another user's /proc/<pid>/comm) rather than an
// error, matching every other best-effort field in models.Inventory.
func processNameForPID(pid int32) string {
	if pid <= 0 {
		return ""
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(int(pid)) + "/comm")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
