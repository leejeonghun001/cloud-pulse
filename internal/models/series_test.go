package models

import (
	"encoding/json"
	"testing"
)

func TestResolutionFor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		from int64
		to   int64
		want int
	}{
		{"zero_span", 0, 0, ResolutionRaw},
		{"exactly_6h", 0, sixHours, ResolutionRaw},
		{"just_over_6h", 0, sixHours + 1, Resolution5m},
		{"exactly_7d", 0, sevenDay, Resolution5m},
		{"just_over_7d", 0, sevenDay + 1, Resolution1h},
		{"large_span", 0, 30 * 24 * 3600, Resolution1h},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ResolutionFor(tc.from, tc.to); got != tc.want {
				t.Errorf("ResolutionFor(%d, %d) = %d, want %d", tc.from, tc.to, got, tc.want)
			}
		})
	}
}

func TestNewSeries_EmptySlicesEncodeAsEmptyArray(t *testing.T) {
	t.Parallel()

	s := NewSeries("host-1", ResolutionRaw, 0, 100)
	if s.Len() != 0 {
		t.Fatalf("Len() = %d, want 0", s.Len())
	}

	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	arrayFields := []string{"ts", "cpu", "mem", "disk", "net_rx", "net_tx", "disk_read", "disk_write", "load1"}
	for _, f := range arrayFields {
		v, ok := raw[f]
		if !ok {
			t.Errorf("missing field %q in JSON", f)
			continue
		}
		if string(v) != "[]" {
			t.Errorf("field %q = %s, want []", f, string(v))
		}
	}
}

func TestSeries_AppendAndLen(t *testing.T) {
	t.Parallel()

	s := NewSeries("host-1", ResolutionRaw, 0, 100)
	s.Append(SeriesPoint{TS: 1, CPU: 10, Mem: 20, Disk: 30, NetRx: 1, NetTx: 2, DiskRead: 3, DiskWrite: 4, Load1: 0.5})
	s.Append(SeriesPoint{TS: 2, CPU: 11, Mem: 21, Disk: 31, NetRx: 2, NetTx: 3, DiskRead: 4, DiskWrite: 5, Load1: 0.6})

	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	if len(s.CPU) != 2 || s.CPU[0] != 10 || s.CPU[1] != 11 {
		t.Errorf("CPU = %v, want [10 11]", s.CPU)
	}
	if len(s.Timestamps) != 2 || s.Timestamps[0] != 1 || s.Timestamps[1] != 2 {
		t.Errorf("Timestamps = %v, want [1 2]", s.Timestamps)
	}
	if len(s.Load1) != 2 || s.Load1[1] != 0.6 {
		t.Errorf("Load1 = %v", s.Load1)
	}
}
