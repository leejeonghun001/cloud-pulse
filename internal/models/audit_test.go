package models

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestAuditEntryJSON(t *testing.T) {
	t.Parallel()

	entry := AuditEntry{
		ID:         1,
		At:         100,
		Actor:      "admin",
		Remote:     "100.64.1.2",
		Action:     AuditAction("pricing_plan.update"),
		EntityType: "pricing_plan",
		EntityID:   "3",
		BeforeJSON: `{"egress_free_gb":100}`,
		AfterJSON:  `{"egress_free_gb":200}`,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	for _, field := range []string{
		"id", "at", "actor", "remote", "action", "entity_type", "entity_id",
		"before_json", "after_json",
	} {
		if _, ok := raw[field]; !ok {
			t.Errorf("missing JSON field %q in %s", field, b)
		}
	}

	var decoded AuditEntry
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, entry) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, entry)
	}
}

func TestAuditEntryJSON_CreationOmitsBeforeJSON(t *testing.T) {
	t.Parallel()

	entry := AuditEntry{ID: 1, At: 100, Actor: "admin", Action: "pricing_plan.create", EntityType: "pricing_plan", EntityID: "1", AfterJSON: `{"name":"x"}`}
	b, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, ok := raw["before_json"]; ok {
		t.Errorf("expected before_json to be omitted for a creation, got %s", b)
	}
}

func TestAuditListViewJSON(t *testing.T) {
	t.Parallel()

	view := AuditListView{
		Entries: []AuditEntry{{ID: 1, At: 100, Actor: "admin", Action: "pricing_plan.create", EntityType: "pricing_plan", EntityID: "1"}},
		HasMore: true,
	}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded AuditListView
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(decoded, view) {
		t.Errorf("round-trip mismatch: got %+v, want %+v", decoded, view)
	}
}

func TestAuditRetentionDays(t *testing.T) {
	t.Parallel()
	if AuditRetentionDays != 400 {
		t.Errorf("AuditRetentionDays = %d, want 400 (SPEC-v0.6 §3 개선 c)", AuditRetentionDays)
	}
}
