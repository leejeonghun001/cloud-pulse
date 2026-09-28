package agent

import (
	"errors"
	"runtime"
	"testing"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

func TestDetectProvider(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "linux" {
		t.Run("non_linux_always_other", func(t *testing.T) {
			t.Parallel()
			got := DetectProvider(func(string) ([]byte, error) { return nil, errors.New("should not be called") })
			if got != models.ProviderOther {
				t.Errorf("DetectProvider on %s = %q, want other", runtime.GOOS, got)
			}
		})
		return
	}

	cases := []struct {
		name  string
		files map[string]string
		want  models.Provider
	}{
		{
			name:  "aws_sys_vendor",
			files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Amazon EC2\n"},
			want:  models.ProviderAWS,
		},
		{
			name:  "aws_board_vendor",
			files: map[string]string{"/sys/class/dmi/id/board_vendor": "Amazon EC2"},
			want:  models.ProviderAWS,
		},
		{
			name:  "aws_bios_vendor",
			files: map[string]string{"/sys/class/dmi/id/bios_vendor": "Amazon EC2"},
			want:  models.ProviderAWS,
		},
		{
			name:  "aws_product_name_prefix",
			files: map[string]string{"/sys/class/dmi/id/product_name": "Amazon EC2 t3.micro"},
			want:  models.ProviderAWS,
		},
		{
			name:  "oci_chassis_asset_tag",
			files: map[string]string{"/sys/class/dmi/id/chassis_asset_tag": "OracleCloud.com"},
			want:  models.ProviderOCI,
		},
		{
			name:  "other_unknown_vendor",
			files: map[string]string{"/sys/class/dmi/id/sys_vendor": "QEMU"},
			want:  models.ProviderOther,
		},
		{
			name:  "other_all_files_missing",
			files: map[string]string{},
			want:  models.ProviderOther,
		},
		{
			name:  "aws_takes_priority_over_oci",
			files: map[string]string{"/sys/class/dmi/id/sys_vendor": "Amazon EC2", "/sys/class/dmi/id/chassis_asset_tag": "OracleCloud.com"},
			want:  models.ProviderAWS,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			readFile := func(path string) ([]byte, error) {
				v, ok := tc.files[path]
				if !ok {
					return nil, errors.New("not found")
				}
				return []byte(v), nil
			}
			got := DetectProvider(readFile)
			if got != tc.want {
				t.Errorf("DetectProvider() = %q, want %q", got, tc.want)
			}
		})
	}
}
