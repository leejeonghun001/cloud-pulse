package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

// goldenAgentReport is a fixed golden JSON document exercising AgentReport's
// full JSON contract (host + one sample). Spot-checked tag names below must
// match exactly; values contain no secrets (plain test fixture data).
const goldenAgentReport = `{
  "host": {
    "id": "web-01",
    "hostname": "web-01.internal",
    "os": "linux",
    "platform": "ubuntu",
    "platform_version": "22.04",
    "kernel_version": "5.15.0",
    "arch": "arm64",
    "cpu_model": "Cortex-A72",
    "cpu_cores": 4,
    "boot_time": 1700000000,
    "provider": "aws",
    "egress_limit_bytes": 107374182400,
    "agent_version": "0.1.0"
  },
  "samples": [
    {
      "ts": 1700000100,
      "cpu_percent": 12.5,
      "load1": 0.1,
      "load5": 0.2,
      "load15": 0.3,
      "mem_total": 1000,
      "mem_available": 400,
      "mem_used": 600,
      "mem_used_percent": 60,
      "mem_cached": 100,
      "swap_total": 0,
      "swap_used": 0,
      "disk_total": 5000,
      "disk_used": 2500,
      "disk_used_percent": 50,
      "disk_read_bps": 10.5,
      "disk_write_bps": 20.5,
      "net_rx_bps": 1.5,
      "net_tx_bps": 2.5,
      "net_rx_bytes": 1500,
      "net_tx_bytes": 2500,
      "uptime_seconds": 3600
    }
  ]
}`

func TestAgentReport_JSONRoundTrip(t *testing.T) {
	t.Parallel()

	var report AgentReport
	if err := json.Unmarshal([]byte(goldenAgentReport), &report); err != nil {
		t.Fatalf("Unmarshal golden: %v", err)
	}

	want := AgentReport{
		Host: HostInfo{
			ID:               "web-01",
			Hostname:         "web-01.internal",
			OS:               "linux",
			Platform:         "ubuntu",
			PlatformVersion:  "22.04",
			KernelVersion:    "5.15.0",
			Arch:             "arm64",
			CPUModel:         "Cortex-A72",
			CPUCores:         4,
			BootTime:         1700000000,
			Provider:         ProviderAWS,
			EgressLimitBytes: 100 * GiB,
			AgentVersion:     "0.1.0",
		},
		Samples: []Sample{
			{
				Timestamp:       1700000100,
				CPUPercent:      12.5,
				Load1:           0.1,
				Load5:           0.2,
				Load15:          0.3,
				MemTotal:        1000,
				MemAvailable:    400,
				MemUsed:         600,
				MemUsedPercent:  60,
				MemCached:       100,
				DiskTotal:       5000,
				DiskUsed:        2500,
				DiskUsedPercent: 50,
				DiskReadBps:     10.5,
				DiskWriteBps:    20.5,
				NetRxBps:        1.5,
				NetTxBps:        2.5,
				NetRxBytes:      1500,
				NetTxBytes:      2500,
				UptimeSeconds:   3600,
			},
		},
	}

	if !reflect.DeepEqual(report, want) {
		t.Fatalf("Unmarshal golden = %+v, want %+v", report, want)
	}

	// Round-trip: marshal back and spot-check exact tag names are present.
	out, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("Unmarshal remarshaled: %v", err)
	}
	if _, ok := raw["ts"]; ok {
		t.Error(`top-level AgentReport must not have a "ts" key`)
	}

	var samples []json.RawMessage
	if err := json.Unmarshal(raw["samples"], &samples); err != nil {
		t.Fatalf("Unmarshal samples: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("len(samples) = %d, want 1", len(samples))
	}

	var sampleFields map[string]json.RawMessage
	if err := json.Unmarshal(samples[0], &sampleFields); err != nil {
		t.Fatalf("Unmarshal sample fields: %v", err)
	}

	spotCheck := []string{"ts", "cpu_percent", "mem_available", "net_tx_bytes"}
	for _, key := range spotCheck {
		if _, ok := sampleFields[key]; !ok {
			t.Errorf("sample missing expected key %q", key)
		}
	}

	var hostFields map[string]json.RawMessage
	if err := json.Unmarshal(raw["host"], &hostFields); err != nil {
		t.Fatalf("Unmarshal host fields: %v", err)
	}
	if _, ok := hostFields["egress_limit_bytes"]; !ok {
		t.Error(`host missing expected key "egress_limit_bytes"`)
	}

	// Re-marshaling must reproduce semantically identical JSON (round trip).
	var report2 AgentReport
	if err := json.Unmarshal(out, &report2); err != nil {
		t.Fatalf("Unmarshal remarshaled into struct: %v", err)
	}
	if !reflect.DeepEqual(report, report2) {
		t.Fatalf("round trip mismatch: %+v != %+v", report, report2)
	}
}

func TestSample_DisksOmittedWhenNil(t *testing.T) {
	t.Parallel()

	s := Sample{Timestamp: 1}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["disks"]; ok {
		t.Error(`"disks" must be omitted when nil`)
	}
}

func TestAPIError_JSON(t *testing.T) {
	t.Parallel()

	t.Run("code_omitted_when_empty", func(t *testing.T) {
		t.Parallel()
		e := APIError{Error: "bad request"}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if string(b) != `{"error":"bad request"}` {
			t.Errorf("Marshal(APIError) = %s, want {\"error\":\"bad request\"}", b)
		}
	})

	t.Run("code_present_when_set", func(t *testing.T) {
		t.Parallel()
		e := APIError{Error: "settings are disabled", Code: "admin_disabled"}
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		want := `{"error":"settings are disabled","code":"admin_disabled"}`
		if string(b) != want {
			t.Errorf("Marshal(APIError) = %s, want %s", b, want)
		}
	})
}

func TestIngestResponse_JSON(t *testing.T) {
	t.Parallel()

	r := IngestResponse{Accepted: 1, Duplicates: 2, Rejected: 3, ServerTimeMs: 1700000000123}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"accepted":1,"duplicates":2,"rejected":3,"server_time_ms":1700000000123}`
	if string(b) != want {
		t.Errorf("Marshal(IngestResponse) = %s, want %s", b, want)
	}
}

func TestTimeResponse_JSON(t *testing.T) {
	t.Parallel()

	r := TimeResponse{ServerTimeMs: 1700000000123}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	want := `{"server_time_ms":1700000000123}`
	if string(b) != want {
		t.Errorf("Marshal(TimeResponse) = %s, want %s", b, want)
	}

	var got TimeResponse
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got != r {
		t.Errorf("round-trip = %+v, want %+v", got, r)
	}
}

func TestAPIError_JSON_DetailsOmittedWhenNil(t *testing.T) {
	t.Parallel()

	e := APIError{Error: "invalid request"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["details"]; ok {
		t.Errorf("details must be omitted when nil, got %s", b)
	}
}

func TestAPIError_JSON_DetailsRoundTrip(t *testing.T) {
	t.Parallel()

	e := APIError{
		Error: "validation failed",
		Code:  "invalid_request",
		Details: map[string]string{
			"threshold":    "must be greater than 0",
			"duration_sec": "must not be negative",
		},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded APIError
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(decoded.Details) != len(e.Details) {
		t.Fatalf("Details = %v, want %v", decoded.Details, e.Details)
	}
	for k, v := range e.Details {
		if decoded.Details[k] != v {
			t.Errorf("Details[%q] = %q, want %q", k, decoded.Details[k], v)
		}
	}
}

func TestAgentReport_InventoryOmittedWhenNil(t *testing.T) {
	t.Parallel()

	r := AgentReport{Host: HostInfo{ID: "h1"}, Samples: []Sample{}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["inventory"]; ok {
		t.Errorf("inventory must be omitted when nil, got %s", b)
	}
}

func TestAgentReport_InventoryRoundTrip(t *testing.T) {
	t.Parallel()

	r := AgentReport{
		Host:    HostInfo{ID: "h1"},
		Samples: []Sample{},
		Inventory: &Inventory{
			CollectedAt: 1700000000,
			Ports:       []ListeningPort{{Proto: "tcp", IP: "0.0.0.0", Port: 22}},
			Docker:      DockerInfo{Status: DockerStatusUnavailable, Containers: []Container{}},
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AgentReport
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Inventory == nil {
		t.Fatal("Inventory = nil, want non-nil")
	}
	if decoded.Inventory.CollectedAt != r.Inventory.CollectedAt {
		t.Errorf("Inventory.CollectedAt = %d, want %d", decoded.Inventory.CollectedAt, r.Inventory.CollectedAt)
	}
	if len(decoded.Inventory.Ports) != 1 || decoded.Inventory.Ports[0].Port != 22 {
		t.Errorf("Inventory.Ports = %+v, want one port 22", decoded.Inventory.Ports)
	}
}

func TestAgentReport_OldPayloadWithoutInventoryStillDecodes(t *testing.T) {
	t.Parallel()

	// Simulates a pre-v0.5.0 agent's payload, which never sent an
	// "inventory" field at all.
	const oldPayload = `{"host":{"id":"h1"},"samples":[]}`
	var r AgentReport
	if err := json.Unmarshal([]byte(oldPayload), &r); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if r.Inventory != nil {
		t.Errorf("Inventory = %+v, want nil for a payload with no inventory field", r.Inventory)
	}
}

func TestAgentReport_UpdateStatusOmittedWhenNil(t *testing.T) {
	t.Parallel()

	r := AgentReport{Host: HostInfo{ID: "h1"}, Samples: []Sample{}}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["update_status"]; ok {
		t.Errorf("update_status must be omitted when nil, got %s", b)
	}
}

func TestAgentReport_UpdateStatusRoundTrip(t *testing.T) {
	t.Parallel()

	r := AgentReport{
		Host:    HostInfo{ID: "h1"},
		Samples: []Sample{},
		UpdateStatus: &AgentUpdateStatus{
			JobID:   42,
			State:   UpdateJobSucceeded,
			Version: "v0.6.0",
		},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AgentReport
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.UpdateStatus == nil || *decoded.UpdateStatus != *r.UpdateStatus {
		t.Errorf("UpdateStatus round-trip = %+v, want %+v", decoded.UpdateStatus, r.UpdateStatus)
	}
}

func TestIngestResponse_UpdateRequestOmittedWhenNil(t *testing.T) {
	t.Parallel()

	r := IngestResponse{Accepted: 1, ServerTimeMs: 100}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["update_request"]; ok {
		t.Errorf("update_request must be omitted when nil, got %s", b)
	}
}

func TestIngestResponse_UpdateRequestRoundTrip(t *testing.T) {
	t.Parallel()

	r := IngestResponse{
		Accepted:      1,
		ServerTimeMs:  100,
		UpdateRequest: &UpdateRequest{JobID: 7, Target: "latest"},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded IngestResponse
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.UpdateRequest == nil || *decoded.UpdateRequest != *r.UpdateRequest {
		t.Errorf("UpdateRequest round-trip = %+v, want %+v", decoded.UpdateRequest, r.UpdateRequest)
	}
}
