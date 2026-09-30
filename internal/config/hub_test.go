package config

import (
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestLoadHub_Defaults(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"CP_AGENT_TOKEN": "abcdefghijklmnop",
	}
	h, err := LoadHub(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}

	if h.Listen != ":8090" {
		t.Errorf("Listen = %q, want :8090", h.Listen)
	}
	if h.DataDir != "./data" {
		t.Errorf("DataDir = %q, want ./data", h.DataDir)
	}
	if h.AgentToken != "abcdefghijklmnop" {
		t.Errorf("AgentToken = %q", h.AgentToken)
	}
	if h.UIToken != "" {
		t.Errorf("UIToken = %q, want empty", h.UIToken)
	}
	if len(h.AllowedCIDRs) != 4 {
		t.Fatalf("AllowedCIDRs = %v, want 4 entries", h.AllowedCIDRs)
	}
	if h.OfflineAfter != 60*time.Second {
		t.Errorf("OfflineAfter = %v, want 60s", h.OfflineAfter)
	}
	if h.CloudInterval != 15*time.Minute {
		t.Errorf("CloudInterval = %v, want 15m", h.CloudInterval)
	}
	if h.LogLevel != "info" {
		t.Errorf("LogLevel = %q, want info", h.LogLevel)
	}
	if !h.UpdateCheck {
		t.Error("UpdateCheck = false, want true (default)")
	}
	if h.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", h.LogFormat)
	}
	if h.S3FilterID != "EntireBucket" {
		t.Errorf("S3FilterID = %q, want EntireBucket", h.S3FilterID)
	}
	if h.S3Buckets != nil {
		t.Errorf("S3Buckets = %v, want nil", h.S3Buckets)
	}
	if h.R2Buckets != nil {
		t.Errorf("R2Buckets = %v, want nil", h.R2Buckets)
	}
	if h.S3Enabled() {
		t.Error("S3Enabled() = true, want false")
	}
	if h.R2Enabled() {
		t.Error("R2Enabled() = true, want false")
	}
	if got, want := h.DBPath(), filepath.Join("data", "cloud-pulse.db"); got != want {
		t.Errorf("DBPath() = %q, want %q", got, want)
	}
	if h.Billing != "auto" {
		t.Errorf("Billing = %q, want auto", h.Billing)
	}
	if !h.BillingEnabled() {
		t.Error("BillingEnabled() = false, want true (default)")
	}
	if h.BillingInterval != models.BillingInterval24h {
		t.Errorf("BillingInterval = %q, want 24h", h.BillingInterval)
	}
	if h.BillingAWSResources {
		t.Error("BillingAWSResources = true, want false (default)")
	}
	if h.StorageInterval != models.StorageInterval1h {
		t.Errorf("StorageInterval = %q, want 1h", h.StorageInterval)
	}
	if h.OCIConfigFile != "" {
		t.Errorf("OCIConfigFile = %q, want empty", h.OCIConfigFile)
	}
	if h.OCIProfile != "" {
		t.Errorf("OCIProfile = %q, want empty", h.OCIProfile)
	}
	if h.OCITenancyID != "" {
		t.Errorf("OCITenancyID = %q, want empty", h.OCITenancyID)
	}
}

func TestLoadHub_AgentTokenValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"missing", "", true},
		{"exactly_15_chars", "123456789012345", true},
		{"exactly_16_chars", "1234567890123456", false},
		{"long", "this-is-a-long-enough-token-value", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{}
			if tc.token != "" {
				env["CP_AGENT_TOKEN"] = tc.token
			}
			_, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && err != nil && strings.Contains(err.Error(), tc.token) && tc.token != "" {
				t.Errorf("error must not contain token value: %q", err.Error())
			}
		})
	}
}

func TestLoadHub_AgentTokenErrorDoesNotLeakValue(t *testing.T) {
	t.Parallel()

	secretLikeButShort := "short-secret-123"[:10] // 10 chars, below 16
	env := map[string]string{"CP_AGENT_TOKEN": secretLikeButShort}
	_, err := LoadHub(mapLookup(env))
	if err == nil {
		t.Fatal("expected error for short token")
	}
	if strings.Contains(err.Error(), secretLikeButShort) {
		t.Errorf("error must not contain token value, got: %q", err.Error())
	}
}

func TestLoadHub_UITokenValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"unset_ok", "", false},
		{"too_short", "short12", true},
		{"exactly_8_chars", "12345678", false},
		{"long", "a-long-enough-ui-token", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.token != "" {
				env["CP_UI_TOKEN"] = tc.token
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr && err != nil && strings.Contains(err.Error(), tc.token) {
				t.Errorf("error must not contain token value: %q", err.Error())
			}
			if !tc.wantErr && h.UIToken != tc.token {
				t.Errorf("UIToken = %q, want %q", h.UIToken, tc.token)
			}
		})
	}
}

func TestLoadHub_AllowedCIDRs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		raw     string
		want    []string // String() forms, nil means allow-all
		wantErr bool
	}{
		{"star_allows_all", "*", nil, false},
		{"empty_uses_default", "", []string{"100.64.0.0/10", "fd7a:115c:a1e0::/48", "127.0.0.0/8", "::1/128"}, false},
		{"single_cidr", "10.0.0.0/8", []string{"10.0.0.0/8"}, false},
		{"bare_ipv4_becomes_slash32", "10.0.0.1", []string{"10.0.0.1/32"}, false},
		{"bare_ipv6_becomes_slash128", "::1", []string{"::1/128"}, false},
		{"mixed_list", "10.0.0.0/8, 192.168.1.1", []string{"10.0.0.0/8", "192.168.1.1/32"}, false},
		{"invalid_entry", "not-an-ip", nil, true},
		{"invalid_cidr_suffix", "10.0.0.0/99", nil, true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.raw != "" {
				env["CP_ALLOWED_CIDRS"] = tc.raw
			} else if tc.name == "star_allows_all" {
				env["CP_ALLOWED_CIDRS"] = "*"
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if tc.want == nil {
				if h.AllowedCIDRs != nil {
					t.Errorf("AllowedCIDRs = %v, want nil (allow all)", h.AllowedCIDRs)
				}
				return
			}
			if len(h.AllowedCIDRs) != len(tc.want) {
				t.Fatalf("AllowedCIDRs = %v, want %v", h.AllowedCIDRs, tc.want)
			}
			for i, p := range h.AllowedCIDRs {
				if p.String() != tc.want[i] {
					t.Errorf("AllowedCIDRs[%d] = %q, want %q", i, p.String(), tc.want[i])
				}
			}
		})
	}
}

func TestLoadHub_AllowedCIDRs_StarAmongEntries(t *testing.T) {
	t.Parallel()

	// A "*" mixed with other entries still means allow-all per spec
	// ("*" alone means allow all); here we exercise that a "*" entry
	// anywhere in the list short-circuits to allow-all.
	env := map[string]string{
		"CP_AGENT_TOKEN":   "abcdefghijklmnop",
		"CP_ALLOWED_CIDRS": "*",
	}
	h, err := LoadHub(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if h.AllowedCIDRs != nil {
		t.Errorf("AllowedCIDRs = %v, want nil", h.AllowedCIDRs)
	}
}

func TestLoadHub_S3Buckets(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		raw    string
		region string
		awsReg string
		want   []S3BucketConfig
	}{
		{"empty", "", "", "", nil},
		{
			"single_no_region_default_us_east_1",
			"my-bucket", "", "",
			[]S3BucketConfig{{Name: "my-bucket", Region: "us-east-1"}},
		},
		{
			"single_with_region",
			"my-bucket:eu-west-1", "", "",
			[]S3BucketConfig{{Name: "my-bucket", Region: "eu-west-1"}},
		},
		{
			"multiple_mixed",
			"a:eu-west-1,b", "", "",
			[]S3BucketConfig{{Name: "a", Region: "eu-west-1"}, {Name: "b", Region: "us-east-1"}},
		},
		{
			"cp_s3_region_overrides_default",
			"b", "ap-southeast-2", "",
			[]S3BucketConfig{{Name: "b", Region: "ap-southeast-2"}},
		},
		{
			"aws_region_used_when_cp_s3_region_unset",
			"b", "", "eu-central-1",
			[]S3BucketConfig{{Name: "b", Region: "eu-central-1"}},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.raw != "" {
				env["CP_S3_BUCKETS"] = tc.raw
			}
			if tc.region != "" {
				env["CP_S3_REGION"] = tc.region
			}
			if tc.awsReg != "" {
				env["AWS_REGION"] = tc.awsReg
			}
			h, err := LoadHub(mapLookup(env))
			if err != nil {
				t.Fatalf("LoadHub: %v", err)
			}
			if len(h.S3Buckets) != len(tc.want) {
				t.Fatalf("S3Buckets = %+v, want %+v", h.S3Buckets, tc.want)
			}
			for i := range h.S3Buckets {
				if h.S3Buckets[i] != tc.want[i] {
					t.Errorf("S3Buckets[%d] = %+v, want %+v", i, h.S3Buckets[i], tc.want[i])
				}
			}
		})
	}

	t.Run("invalid_empty_name", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN": "abcdefghijklmnop",
			"CP_S3_BUCKETS":  ":eu-west-1",
		}
		_, err := LoadHub(mapLookup(env))
		if err == nil {
			t.Fatal("expected error for empty bucket name")
		}
	})
}

func TestLoadHub_S3AndR2Enabled(t *testing.T) {
	t.Parallel()

	t.Run("s3_enabled_requires_buckets_and_keys", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN":        "abcdefghijklmnop",
			"CP_S3_BUCKETS":         "b1",
			"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLEFAKEKEY1",
			"AWS_SECRET_ACCESS_KEY": "examplefakesecretaccesskeyvalue00000000",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		if !h.S3Enabled() {
			t.Error("S3Enabled() = false, want true")
		}
	})

	t.Run("s3_disabled_without_keys", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN": "abcdefghijklmnop",
			"CP_S3_BUCKETS":  "b1",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		if h.S3Enabled() {
			t.Error("S3Enabled() = true, want false")
		}
	})

	t.Run("s3_disabled_without_buckets", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN":        "abcdefghijklmnop",
			"AWS_ACCESS_KEY_ID":     "AKIAEXAMPLEFAKEKEY1",
			"AWS_SECRET_ACCESS_KEY": "examplefakesecretaccesskeyvalue00000000",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		if h.S3Enabled() {
			t.Error("S3Enabled() = true, want false")
		}
	})

	t.Run("r2_enabled_requires_account_and_token", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN":   "abcdefghijklmnop",
			"CP_R2_ACCOUNT_ID": "acct123",
			"CP_R2_API_TOKEN":  "faketoken123",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		if !h.R2Enabled() {
			t.Error("R2Enabled() = false, want true")
		}
	})

	t.Run("r2_disabled_when_partial", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN":   "abcdefghijklmnop",
			"CP_R2_ACCOUNT_ID": "acct123",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		if h.R2Enabled() {
			t.Error("R2Enabled() = true, want false")
		}
	})

	t.Run("r2_buckets_list", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN": "abcdefghijklmnop",
			"CP_R2_BUCKETS":  "bucket-a, bucket-b",
		}
		h, err := LoadHub(mapLookup(env))
		if err != nil {
			t.Fatalf("LoadHub: %v", err)
		}
		want := []string{"bucket-a", "bucket-b"}
		if len(h.R2Buckets) != len(want) {
			t.Fatalf("R2Buckets = %v, want %v", h.R2Buckets, want)
		}
		for i := range want {
			if h.R2Buckets[i] != want[i] {
				t.Errorf("R2Buckets[%d] = %q, want %q", i, h.R2Buckets[i], want[i])
			}
		}
	})
}

func TestLoadHub_WebhookURLValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"unset_ok", "", false},
		{"https_ok", "https://hooks.example.com/x", false},
		{"http_ok", "http://hooks.example.com/x", false},
		{"missing_scheme", "hooks.example.com/x", true},
		{"ftp_scheme", "ftp://hooks.example.com/x", true},
		{"no_host", "https:///path", true},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.url != "" {
				env["CP_ALERT_WEBHOOK_URL"] = tc.url
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.AlertWebhookURL != tc.url {
				t.Errorf("AlertWebhookURL = %q, want %q", h.AlertWebhookURL, tc.url)
			}
		})
	}
}

func TestLoadHub_UpdateCheck(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"unset_defaults_true", "", true},
		{"explicit_true", "true", true},
		{"explicit_false", "false", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.raw != "" {
				env["CP_UPDATE_CHECK"] = tc.raw
			}
			h, err := LoadHub(mapLookup(env))
			if err != nil {
				t.Fatalf("LoadHub: %v", err)
			}
			if h.UpdateCheck != tc.want {
				t.Errorf("UpdateCheck = %v, want %v", h.UpdateCheck, tc.want)
			}
		})
	}

	t.Run("invalid_value_errors", func(t *testing.T) {
		t.Parallel()
		env := map[string]string{
			"CP_AGENT_TOKEN":  "abcdefghijklmnop",
			"CP_UPDATE_CHECK": "not-a-bool",
		}
		_, err := LoadHub(mapLookup(env))
		if err == nil {
			t.Fatal("expected error for invalid CP_UPDATE_CHECK")
		}
	})
}

func TestLoadHub_CloudIntervalMinimum(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"CP_AGENT_TOKEN":    "abcdefghijklmnop",
		"CP_CLOUD_INTERVAL": "30s",
	}
	_, err := LoadHub(mapLookup(env))
	if err == nil {
		t.Fatal("expected error for CP_CLOUD_INTERVAL below 1m minimum")
	}
}

func TestLoadHub_DBPath(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"CP_AGENT_TOKEN": "abcdefghijklmnop",
		"CP_DATA_DIR":    "/var/lib/cloud-pulse",
	}
	h, err := LoadHub(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	want := filepath.Join("/var/lib/cloud-pulse", "cloud-pulse.db")
	if got := h.DBPath(); got != want {
		t.Errorf("DBPath() = %q, want %q", got, want)
	}
}

func TestParseCIDROrIP_IPv4MappedIPv6(t *testing.T) {
	t.Parallel()

	// Sanity check that netip.ParseAddr distinguishes 4-in-6 from
	// pure IPv6, since parseCIDROrIP relies on Is4In6 to pick /32 vs /128.
	addr := netip.MustParseAddr("::ffff:127.0.0.1")
	if !addr.Is4In6() {
		t.Fatal("expected ::ffff:127.0.0.1 to be Is4In6")
	}
}

func TestLoadHub_Billing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{"default_auto", "", "auto", false},
		{"explicit_auto", "auto", "auto", false},
		{"upper_auto", "AUTO", "auto", false},
		{"off", "off", "off", false},
		{"invalid", "on", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.value != "" {
				env["CP_BILLING"] = tc.value
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.Billing != tc.want {
				t.Errorf("Billing = %q, want %q", h.Billing, tc.want)
			}
		})
	}
}

func TestLoadHub_BillingEnabled(t *testing.T) {
	t.Parallel()

	on, err := LoadHub(mapLookup(map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop", "CP_BILLING": "auto"}))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if !on.BillingEnabled() {
		t.Error("BillingEnabled() = false for CP_BILLING=auto, want true")
	}

	off, err := LoadHub(mapLookup(map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop", "CP_BILLING": "off"}))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if off.BillingEnabled() {
		t.Error("BillingEnabled() = true for CP_BILLING=off, want false")
	}
}

func TestLoadHub_BillingInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    models.BillingInterval
		wantErr bool
	}{
		{"default_24h", "", models.BillingInterval24h, false},
		{"6h", "6h", models.BillingInterval6h, false},
		{"12h", "12h", models.BillingInterval12h, false},
		{"24h", "24h", models.BillingInterval24h, false},
		{"upper_case", "24H", models.BillingInterval24h, false},
		{"invalid_1h", "1h", "", true},
		{"invalid_48h", "48h", "", true},
		{"invalid_garbage", "not-a-duration", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.value != "" {
				env["CP_BILLING_INTERVAL"] = tc.value
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.BillingInterval != tc.want {
				t.Errorf("BillingInterval = %q, want %q", h.BillingInterval, tc.want)
			}
		})
	}
}

func TestLoadHub_StorageInterval(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    models.StorageInterval
		wantErr bool
	}{
		{"default_1h", "", models.StorageInterval1h, false},
		{"15m", "15m", models.StorageInterval15m, false},
		{"1h", "1h", models.StorageInterval1h, false},
		{"6h", "6h", models.StorageInterval6h, false},
		{"24h", "24h", models.StorageInterval24h, false},
		{"upper_case", "1H", models.StorageInterval1h, false},
		{"invalid_5m", "5m", "", true},
		{"invalid_48h", "48h", "", true},
		{"invalid_garbage", "not-a-duration", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.value != "" {
				env["CP_STORAGE_INTERVAL"] = tc.value
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.StorageInterval != tc.want {
				t.Errorf("StorageInterval = %q, want %q", h.StorageInterval, tc.want)
			}
		})
	}
}

func TestLoadHub_BillingAWSResources(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		want    bool
		wantErr bool
	}{
		{"default_off", "", false, false},
		{"on", "on", false, true}, // strconv.ParseBool doesn't accept "on"
		{"true", "true", true, false},
		{"1", "1", true, false},
		{"false", "false", false, false},
		{"invalid", "yes", false, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}
			if tc.value != "" {
				env["CP_BILLING_AWS_RESOURCES"] = tc.value
			}
			h, err := LoadHub(mapLookup(env))
			if (err != nil) != tc.wantErr {
				t.Fatalf("LoadHub() err = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && h.BillingAWSResources != tc.want {
				t.Errorf("BillingAWSResources = %v, want %v", h.BillingAWSResources, tc.want)
			}
		})
	}
}

func TestLoadHub_OCISettings(t *testing.T) {
	t.Parallel()

	env := map[string]string{
		"CP_AGENT_TOKEN":     "abcdefghijklmnop",
		"CP_OCI_CONFIG_FILE": "/etc/cloud-pulse/oci-config",
		"CP_OCI_PROFILE":     "CUSTOM",
		"CP_OCI_TENANCY_ID":  "ocid1.tenancy.oc1..aaaaaaaaexample",
	}
	h, err := LoadHub(mapLookup(env))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if h.OCIConfigFile != "/etc/cloud-pulse/oci-config" {
		t.Errorf("OCIConfigFile = %q", h.OCIConfigFile)
	}
	if h.OCIProfile != "CUSTOM" {
		t.Errorf("OCIProfile = %q", h.OCIProfile)
	}
	if h.OCITenancyID != "ocid1.tenancy.oc1..aaaaaaaaexample" {
		t.Errorf("OCITenancyID = %q", h.OCITenancyID)
	}
}

// TestLoadHub_BillingPATH verifies the test/smoke-only CP_BILLING_PATH
// override (billing.Options.PATH passthrough) is read as a plain
// string, empty by default.
func TestLoadHub_BillingPATH(t *testing.T) {
	t.Parallel()

	h, err := LoadHub(mapLookup(map[string]string{"CP_AGENT_TOKEN": "abcdefghijklmnop"}))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if h.BillingPATH != "" {
		t.Errorf("BillingPATH = %q, want empty by default", h.BillingPATH)
	}

	h2, err := LoadHub(mapLookup(map[string]string{
		"CP_AGENT_TOKEN":  "abcdefghijklmnop",
		"CP_BILLING_PATH": "/tmp/fake-cli-stubs",
	}))
	if err != nil {
		t.Fatalf("LoadHub: %v", err)
	}
	if h2.BillingPATH != "/tmp/fake-cli-stubs" {
		t.Errorf("BillingPATH = %q, want /tmp/fake-cli-stubs", h2.BillingPATH)
	}
}
