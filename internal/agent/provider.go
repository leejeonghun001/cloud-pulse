package agent

import (
	"runtime"
	"strings"

	"github.com/leejeonghun001/cloud-pulse/internal/models"
)

// dmiFiles lists the /sys/class/dmi/id files inspected for provider
// detection on Linux, in the order they're read.
var dmiFiles = []string{
	"/sys/class/dmi/id/sys_vendor",
	"/sys/class/dmi/id/board_vendor",
	"/sys/class/dmi/id/bios_vendor",
	"/sys/class/dmi/id/product_name",
	"/sys/class/dmi/id/chassis_asset_tag",
}

// DetectProvider attempts to determine the cloud provider a host is
// running on by inspecting Linux DMI identification files via readFile.
// On non-Linux systems it always returns models.ProviderOther.
//
// Detection rules (Linux only): sys_vendor, board_vendor, or bios_vendor
// containing "Amazon EC2", or product_name starting with "Amazon" ->
// ProviderAWS; chassis_asset_tag containing "OracleCloud.com" ->
// ProviderOCI; otherwise ProviderOther.
func DetectProvider(readFile func(string) ([]byte, error)) models.Provider {
	if runtime.GOOS != "linux" {
		return models.ProviderOther
	}

	values := make(map[string]string, len(dmiFiles))
	for _, f := range dmiFiles {
		b, err := readFile(f)
		if err != nil {
			continue
		}
		values[f] = strings.TrimSpace(string(b))
	}

	sysVendor := values["/sys/class/dmi/id/sys_vendor"]
	boardVendor := values["/sys/class/dmi/id/board_vendor"]
	biosVendor := values["/sys/class/dmi/id/bios_vendor"]
	productName := values["/sys/class/dmi/id/product_name"]
	chassisTag := values["/sys/class/dmi/id/chassis_asset_tag"]

	if strings.Contains(sysVendor, "Amazon EC2") ||
		strings.Contains(boardVendor, "Amazon EC2") ||
		strings.Contains(biosVendor, "Amazon EC2") ||
		strings.HasPrefix(productName, "Amazon") {
		return models.ProviderAWS
	}
	if strings.Contains(chassisTag, "OracleCloud.com") {
		return models.ProviderOCI
	}
	return models.ProviderOther
}
