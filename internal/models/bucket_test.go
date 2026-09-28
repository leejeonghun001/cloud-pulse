package models

import (
	"encoding/json"
	"testing"
)

func TestR2FreeTier(t *testing.T) {
	t.Parallel()

	if R2FreeTier.StorageBytes != 10*GiB {
		t.Errorf("StorageBytes = %d, want %d", R2FreeTier.StorageBytes, 10*GiB)
	}
	if R2FreeTier.ClassAOps != 1_000_000 {
		t.Errorf("ClassAOps = %d, want 1000000", R2FreeTier.ClassAOps)
	}
	if R2FreeTier.ClassBOps != 10_000_000 {
		t.Errorf("ClassBOps = %d, want 10000000", R2FreeTier.ClassBOps)
	}
}

func TestBucketView_JSONTags(t *testing.T) {
	t.Parallel()

	view := BucketView{
		Latest:  BucketStats{Provider: StorageR2, Bucket: "my-bucket"},
		History: []BucketPoint{{Timestamp: 1}},
		FreeTier: &FreeTier{
			StorageBytes: R2FreeTier.StorageBytes,
			ClassAOps:    R2FreeTier.ClassAOps,
			ClassBOps:    R2FreeTier.ClassBOps,
		},
	}

	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"latest", "history", "free_tier"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing key %q", key)
		}
	}
}

func TestBucketView_FreeTierOmittedWhenNil(t *testing.T) {
	t.Parallel()

	view := BucketView{Latest: BucketStats{Provider: StorageS3, Bucket: "b"}, History: nil}
	b, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if _, ok := raw["free_tier"]; ok {
		t.Error("free_tier should be omitted when nil")
	}
}
