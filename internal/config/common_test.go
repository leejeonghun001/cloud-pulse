package config

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestGetString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		env  map[string]string
		key  string
		def  string
		want string
	}{
		{"unset", map[string]string{}, "FOO", "default", "default"},
		{"empty", map[string]string{"FOO": ""}, "FOO", "default", "default"},
		{"whitespace_only", map[string]string{"FOO": "   "}, "FOO", "default", "default"},
		{"set", map[string]string{"FOO": "bar"}, "FOO", "default", "bar"},
		{"trims_surrounding_space", map[string]string{"FOO": "  bar  "}, "FOO", "default", "bar"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := getString(mapLookup(tc.env), tc.key, tc.def); got != tc.want {
				t.Errorf("getString(%v, %q, %q) = %q, want %q", tc.env, tc.key, tc.def, got, tc.want)
			}
		})
	}
}

func TestGetDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     map[string]string
		def     time.Duration
		min     time.Duration
		want    time.Duration
		wantErr bool
	}{
		{"unset_returns_default", map[string]string{}, 15 * time.Second, 5 * time.Second, 15 * time.Second, false},
		{"empty_returns_default", map[string]string{"D": ""}, 15 * time.Second, 5 * time.Second, 15 * time.Second, false},
		{"valid_above_min", map[string]string{"D": "30s"}, 15 * time.Second, 5 * time.Second, 30 * time.Second, false},
		{"exactly_min_ok", map[string]string{"D": "5s"}, 15 * time.Second, 5 * time.Second, 5 * time.Second, false},
		{"below_min_errors", map[string]string{"D": "1s"}, 15 * time.Second, 5 * time.Second, 0, true},
		{"unparsable_errors", map[string]string{"D": "not-a-duration"}, 15 * time.Second, 5 * time.Second, 0, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := getDuration(mapLookup(tc.env), "D", tc.def, tc.min)
			if (err != nil) != tc.wantErr {
				t.Fatalf("getDuration err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				if !strings.Contains(err.Error(), "config:") {
					t.Errorf("error %q must be prefixed with config:", err.Error())
				}
				return
			}
			if got != tc.want {
				t.Errorf("getDuration = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("below_min_error_mentions_key_and_min", func(t *testing.T) {
		t.Parallel()
		_, err := getDuration(mapLookup(map[string]string{"CP_INTERVAL": "1s"}), "CP_INTERVAL", 15*time.Second, 5*time.Second)
		if err == nil {
			t.Fatal("expected error")
		}
		if !strings.Contains(err.Error(), "CP_INTERVAL") || !strings.Contains(err.Error(), "must be >=") {
			t.Errorf("error = %q, want it to mention CP_INTERVAL and 'must be >='", err.Error())
		}
	})
}

func TestGetBool(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     map[string]string
		def     bool
		want    bool
		wantErr bool
	}{
		{"unset", map[string]string{}, true, true, false},
		{"empty", map[string]string{"B": ""}, true, true, false},
		{"true", map[string]string{"B": "true"}, false, true, false},
		{"false", map[string]string{"B": "false"}, true, false, false},
		{"1", map[string]string{"B": "1"}, false, true, false},
		{"0", map[string]string{"B": "0"}, true, false, false},
		{"invalid", map[string]string{"B": "yes"}, false, false, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := getBool(mapLookup(tc.env), "B", tc.def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("getBool err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("getBool = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetInt(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     map[string]string
		def     int
		want    int
		wantErr bool
	}{
		{"unset", map[string]string{}, 42, 42, false},
		{"empty", map[string]string{"N": ""}, 42, 42, false},
		{"valid", map[string]string{"N": "7"}, 42, 7, false},
		{"negative", map[string]string{"N": "-3"}, 0, -3, false},
		{"invalid", map[string]string{"N": "abc"}, 0, 0, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := getInt(mapLookup(tc.env), "N", tc.def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("getInt err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("getInt = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGetFloat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		env     map[string]string
		def     float64
		want    float64
		wantErr bool
	}{
		{"unset", map[string]string{}, 1.5, 1.5, false},
		{"empty", map[string]string{"F": ""}, 1.5, 1.5, false},
		{"valid", map[string]string{"F": "3.25"}, 1.5, 3.25, false},
		{"invalid", map[string]string{"F": "abc"}, 0, 0, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := getFloat(mapLookup(tc.env), "F", tc.def)
			if (err != nil) != tc.wantErr {
				t.Fatalf("getFloat err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.want {
				t.Errorf("getFloat = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSplitList(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"whitespace_only", "   ", nil},
		{"single", "a", []string{"a"}},
		{"multiple", "a,b,c", []string{"a", "b", "c"}},
		{"trims_and_drops_empties", " a , , b ,", []string{"a", "b"}},
		{"all_empty_after_trim", " , , ", nil},
		{"globs", "lo,docker*,veth*", []string{"lo", "docker*", "veth*"}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := splitList(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("splitList(%q) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("splitList(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
				}
			}
			if tc.want == nil && got != nil {
				t.Errorf("splitList(%q) = %#v, want nil", tc.in, got)
			}
		})
	}
}

func TestNewLogger(t *testing.T) {
	t.Parallel()

	t.Run("valid_combinations", func(t *testing.T) {
		t.Parallel()
		levels := []string{"debug", "Debug", "INFO", "warn", "Error", ""}
		formats := []string{"text", "JSON", "Text", "json", ""}

		for _, lvl := range levels {
			for _, fmtName := range formats {
				lvl, fmtName := lvl, fmtName
				t.Run(lvl+"_"+fmtName, func(t *testing.T) {
					t.Parallel()
					var buf bytes.Buffer
					logger, err := NewLogger(&buf, lvl, fmtName)
					if err != nil {
						t.Fatalf("NewLogger(%q, %q) error = %v", lvl, fmtName, err)
					}
					if logger == nil {
						t.Fatal("NewLogger returned nil logger")
					}
					logger.Error("hello", "key", "value")
					if buf.Len() == 0 {
						t.Error("expected log output")
					}
				})
			}
		}
	})

	t.Run("invalid_level", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		_, err := NewLogger(&buf, "trace", "text")
		if err == nil {
			t.Fatal("expected error for invalid level")
		}
		if !strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("error = %q, want prefix 'config:'", err.Error())
		}
	})

	t.Run("invalid_format", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		_, err := NewLogger(&buf, "info", "xml")
		if err == nil {
			t.Fatal("expected error for invalid format")
		}
		if !strings.HasPrefix(err.Error(), "config:") {
			t.Errorf("error = %q, want prefix 'config:'", err.Error())
		}
	})

	t.Run("json_format_produces_json", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		logger, err := NewLogger(&buf, "info", "json")
		if err != nil {
			t.Fatalf("NewLogger: %v", err)
		}
		logger.Info("hello")
		out := buf.String()
		if !strings.HasPrefix(strings.TrimSpace(out), "{") {
			t.Errorf("json format output = %q, want it to start with '{'", out)
		}
	})

	t.Run("debug_level_filters_correctly", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		logger, err := NewLogger(&buf, "warn", "text")
		if err != nil {
			t.Fatalf("NewLogger: %v", err)
		}
		logger.Info("should be filtered")
		if buf.Len() != 0 {
			t.Errorf("expected no output for info level under warn threshold, got %q", buf.String())
		}
		logger.Warn("should appear")
		if buf.Len() == 0 {
			t.Error("expected output for warn level")
		}
	})
}
