package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseEnvFile(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    map[string]string
		wantErr bool
	}{
		{
			name:    "simple",
			content: "CP_HUB_URL=http://example.invalid:8090\nCP_AGENT_TOKEN=abcdefghijklmnop\n",
			want: map[string]string{
				"CP_HUB_URL":     "http://example.invalid:8090",
				"CP_AGENT_TOKEN": "abcdefghijklmnop",
			},
		},
		{
			name:    "comments_and_blank_lines",
			content: "# this is a comment\n\nCP_HOST_ID=host1\n   # indented comment\n\n",
			want:    map[string]string{"CP_HOST_ID": "host1"},
		},
		{
			name:    "hash_comment_no_space",
			content: "#comment\nCP_A=1\n",
			want:    map[string]string{"CP_A": "1"},
		},
		{
			name:    "no_shell_expansion",
			content: "CP_A=$HOME\nCP_B=`echo hi`\nCP_C=~/foo\n",
			want:    map[string]string{"CP_A": "$HOME", "CP_B": "`echo hi`", "CP_C": "~/foo"},
		},
		{
			name:    "no_quote_interpretation",
			content: `CP_A="quoted value"` + "\n" + `CP_B='single quoted'` + "\n",
			want:    map[string]string{"CP_A": `"quoted value"`, "CP_B": `'single quoted'`},
		},
		{
			name:    "value_with_equals_sign",
			content: "CP_A=key=value=more\n",
			want:    map[string]string{"CP_A": "key=value=more"},
		},
		{
			name:    "empty_value",
			content: "CP_A=\n",
			want:    map[string]string{"CP_A": ""},
		},
		{
			name:    "windows_crlf",
			content: "CP_A=1\r\nCP_B=2\r\n# comment\r\n\r\nCP_C=3\r\n",
			want:    map[string]string{"CP_A": "1", "CP_B": "2", "CP_C": "3"},
		},
		{
			name:    "duplicate_key_keeps_last",
			content: "CP_A=first\nCP_A=second\n",
			want:    map[string]string{"CP_A": "second"},
		},
		{
			name:    "key_with_leading_trailing_whitespace_trimmed",
			content: "  CP_A  =value\n",
			want:    map[string]string{"CP_A": "value"},
		},
		{
			name:    "value_leading_whitespace_preserved",
			content: "CP_A=  value with spaces  \n",
			want:    map[string]string{"CP_A": "  value with spaces  "},
		},
		{
			name:    "no_trailing_newline",
			content: "CP_A=1\nCP_B=2",
			want:    map[string]string{"CP_A": "1", "CP_B": "2"},
		},
		{
			name:    "underscore_and_digits_in_key",
			content: "_FOO_1=bar\n",
			want:    map[string]string{"_FOO_1": "bar"},
		},
		{
			name:    "missing_equals_is_error",
			content: "CP_A\n",
			wantErr: true,
		},
		{
			name:    "empty_key_is_error",
			content: "=value\n",
			wantErr: true,
		},
		{
			name:    "key_starting_with_digit_is_error",
			content: "1FOO=bar\n",
			wantErr: true,
		},
		{
			name:    "key_with_invalid_char_is_error",
			content: "CP-A=bar\n",
			wantErr: true,
		},
		{
			name:    "control_char_in_value_is_error",
			content: "CP_A=bad\x01value\n",
			wantErr: true,
		},
		{
			name:    "null_byte_is_error",
			content: "CP_A=bad\x00value\n",
			wantErr: true,
		},
		{
			name:    "empty_content",
			content: "",
			want:    map[string]string{},
		},
		{
			name:    "only_comments_and_blanks",
			content: "# nothing here\n\n   \n",
			want:    map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseEnvFile(tt.content)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseEnvFile() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseEnvFile() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseEnvFile_ErrorNamesLineNumber(t *testing.T) {
	_, err := ParseEnvFile("CP_A=1\nCP_B\nCP_C=3\n")
	if err == nil {
		t.Fatal("expected an error for missing '=' on line 2")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error = %q, want it to mention line 2", err.Error())
	}
}

func TestLookupFuncFromEnvFile(t *testing.T) {
	lookup, err := LookupFuncFromEnvFile("CP_HUB_URL=http://example.invalid:8090\nCP_AGENT_TOKEN=abcdefghijklmnop\n")
	if err != nil {
		t.Fatalf("LookupFuncFromEnvFile: %v", err)
	}

	v, ok := lookup("CP_HUB_URL")
	if !ok || v != "http://example.invalid:8090" {
		t.Errorf("lookup(CP_HUB_URL) = (%q, %v), want (http://example.invalid:8090, true)", v, ok)
	}

	_, ok = lookup("CP_MISSING")
	if ok {
		t.Error("lookup(CP_MISSING) ok = true, want false")
	}
}

func TestLookupFuncFromEnvFile_PropagatesParseError(t *testing.T) {
	_, err := LookupFuncFromEnvFile("not valid\n")
	if err == nil {
		t.Fatal("expected a parse error to propagate")
	}
}

// TestLookupFuncFromEnvFile_UsableByLoadAgent is an integration-style
// check that a LookupFunc built from a parsed env file works as a drop
// in replacement for os.LookupEnv when loading agent config end to end
// (SPEC-v0.7 §1's whole motivation for this parser).
func TestLookupFuncFromEnvFile_UsableByLoadAgent(t *testing.T) {
	content := "CP_HUB_URL=http://example.invalid:8090\n" +
		"CP_AGENT_TOKEN=abcdefghijklmnop\n" +
		"CP_HOST_ID=test-host\n"
	lookup, err := LookupFuncFromEnvFile(content)
	if err != nil {
		t.Fatalf("LookupFuncFromEnvFile: %v", err)
	}
	cfg, err := LoadAgent(lookup, "fallback-hostname")
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if cfg.HubURL != "http://example.invalid:8090" {
		t.Errorf("HubURL = %q, want http://example.invalid:8090", cfg.HubURL)
	}
	if cfg.HostID != "test-host" {
		t.Errorf("HostID = %q, want test-host", cfg.HostID)
	}
}
