package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestUpdateJobJSON(t *testing.T) {
	t.Parallel()

	job := UpdateJob{
		ID:        1,
		BatchID:   "batch-abc123",
		HostID:    "host-1",
		Target:    "latest",
		State:     UpdateJobFailed,
		Reason:    UpdateReasonTimeout,
		Error:     "no acknowledgement within 15m",
		CreatedAt: 100,
		UpdatedAt: 200,
		Attempt:   2,
	}
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"id", "batch_id", "host_id", "target", "state", "reason", "error",
		"created_at", "updated_at", "attempt",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded UpdateJob
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, job) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, job)
	}
}

func TestUpdateJobJSON_QueuedOmitsReasonAndError(t *testing.T) {
	t.Parallel()

	job := UpdateJob{ID: 1, BatchID: "b1", HostID: "host-1", Target: "latest", State: UpdateJobQueued}
	b, err := json.Marshal(job)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{"reason", "error"} {
		if _, ok := raw[field]; ok {
			t.Errorf("expected field %q to be omitted for a queued job, got %s", field, b)
		}
	}
}

func TestUpdateBatchJSON(t *testing.T) {
	t.Parallel()

	batch := UpdateBatch{
		BatchID:     "batch-abc123",
		Target:      "v0.6.0",
		MaxParallel: 3,
		CreatedAt:   100,
		Jobs: []UpdateJob{
			{ID: 1, BatchID: "batch-abc123", HostID: "host-1", Target: "v0.6.0", State: UpdateJobQueued},
			{ID: 2, BatchID: "batch-abc123", HostID: "host-2", Target: "v0.6.0", State: UpdateJobInProgress},
		},
	}
	b, err := json.Marshal(batch)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded UpdateBatch
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, batch) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, batch)
	}
}

func TestUpdateBatchCreateJSON(t *testing.T) {
	t.Parallel()

	req := UpdateBatchCreate{HostIDs: []string{"host-1", "host-2"}, Target: "latest", MaxParallel: 5}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded UpdateBatchCreate
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, req)
	}
}

func TestUpdateRequestJSON(t *testing.T) {
	t.Parallel()

	req := UpdateRequest{JobID: 42, Target: "v0.6.0"}
	b, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded UpdateRequest
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, req) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, req)
	}
}

func TestAgentUpdateStatusJSON(t *testing.T) {
	t.Parallel()

	st := AgentUpdateStatus{JobID: 42, State: UpdateJobFailed, Version: "v0.5.0", ErrorCode: UpdateReasonChecksumMismatch, Error: "sha256 mismatch"}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AgentUpdateStatus
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, st) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, st)
	}
}

func TestRemoteUpdateCapabilityJSON(t *testing.T) {
	t.Parallel()

	tests := []RemoteUpdateCapability{
		{Supported: true, OptedIn: true},
		{Supported: true, OptedIn: false, Reason: UpdateReasonNotEnabled},
		{Supported: false, OptedIn: false, Reason: UpdateReasonUnsupported},
	}
	for _, rc := range tests {
		b, err := json.Marshal(rc)
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		var decoded RemoteUpdateCapability
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if !reflect.DeepEqual(decoded, rc) {
			t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, rc)
		}
	}
}
