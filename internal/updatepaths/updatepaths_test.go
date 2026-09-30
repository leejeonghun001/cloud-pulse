package updatepaths

import "testing"

func TestFor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		goos string
		want Paths
		ok   bool
	}{
		{"linux", Paths{"/var/lib/cloud-pulse-agent", "/var/lib/cloud-pulse-agent/update-request.json", "/var/lib/cloud-pulse-agent-update"}, true},
		{"darwin", Paths{"/Library/Application Support/cloud-pulse-agent", "/Library/Application Support/cloud-pulse-agent/update-request.json", "/Library/Application Support/cloud-pulse-agent-update"}, true},
		{"windows", Paths{`C:\ProgramData\cloud-pulse-agent`, `C:\ProgramData\cloud-pulse-agent\update-request.json`, `C:\ProgramData\cloud-pulse-agent-update`}, true},
		{"freebsd", Paths{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			got, ok := For(tc.goos)
			if ok != tc.ok || got != tc.want {
				t.Errorf("For(%q) = (%+v, %v), want (%+v, %v)", tc.goos, got, ok, tc.want, tc.ok)
			}
		})
	}
}
