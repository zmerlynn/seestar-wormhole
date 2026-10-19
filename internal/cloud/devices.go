package cloud

import "log/slog"

// maxDeviceItems bounds retained identities independently of server metadata.
const maxDeviceItems = 256

// ParseDevices reads a device catalog page, retaining at most 256 distinct
// identities in first-seen order. Total is a pagination hint, or -1 when absent,
// null, negative or smaller than the wire item count on this page.
// Absent/null items form an empty page. Local client policy:
// key by (deviceSn, deviceModel), keep the first exact pair,
// and log duplicates. Status disagreements do not affect selection.
func ParseDevices(b []byte) ([]Detail, int, error) {
	return ParseDevicesWithLogger(b, nil)
}

// ParseDevicesWithLogger parses a page like ParseDevices and reports tolerated
// oddities at debug using fixed metadata, without logging identities or input.
func ParseDevicesWithLogger(b []byte, logger *slog.Logger) ([]Detail, int, error) {
	page, err := ParseDevicePageWithLogger(b, logger)
	return page.Devices, page.Total, err
}

// DevicePage retains paging metadata and wire item counts so local client policy
// can establish catalog completeness independently of pair deduplication.
type DevicePage struct {
	Devices   []Detail
	ItemCount int // Wire items, including duplicates; null/absent items count as zero.
	Total     int // -1 when absent, null, negative or below ItemCount.
	Current   int // Positive reply current, or zero for the requested-page fallback.
}

// ParseDevicePageWithLogger parses a catalog page and its paging metadata,
// logging tolerated oddities at debug without identities or raw input.
// R13c/R13d define page metadata, first-pair retention and completeness:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#device-selection
func ParseDevicePageWithLogger(b []byte, logger *slog.Logger) (DevicePage, error) {
	r, err := readResponse(b)
	if err != nil {
		return DevicePage{}, err
	}
	if err := cloudCode(r); err != nil {
		return DevicePage{}, err
	}
	var d struct {
		Items   []deviceIdentity `json:"items"`
		Total   *int             `json:"total"`
		Current *int             `json:"current"`
	}
	if err := decodeJSON(r.Data, &d); err != nil {
		return DevicePage{}, err
	}
	devices := make([]Detail, 0, min(len(d.Items), maxDeviceItems))
	seen := make(map[[2]string]bool, min(len(d.Items), maxDeviceItems))
	duplicates := 0
	for _, item := range d.Items {
		device, err := item.detail()
		if err != nil {
			return DevicePage{}, err
		}
		pair := [2]string{device.Serial(), device.Model()}
		if seen[pair] {
			duplicates++
			continue
		}
		if len(devices) == maxDeviceItems {
			return DevicePage{}, invalid("data.items", "entry_limit")
		}
		seen[pair] = true
		devices = append(devices, device)
	}
	total := -1
	ignoredTotal := false
	if d.Total != nil {
		if *d.Total < len(d.Items) || *d.Total < 0 {
			ignoredTotal = true
		} else {
			total = *d.Total
		}
	}
	if logger != nil && (ignoredTotal || d.Items == nil || duplicates > 0) {
		logger.Debug("cloud_catalog_oddities", "total_hint_ignored", ignoredTotal,
			"empty_items", d.Items == nil, "duplicate_items", duplicates)
	}
	current := 0
	if d.Current != nil && *d.Current > 0 {
		current = *d.Current
	}
	return DevicePage{Devices: devices, ItemCount: len(d.Items), Total: total, Current: current}, nil
}
