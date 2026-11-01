package cloudhttp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// SelectDevice resolves serial and model from the account catalog, or verifies
// an explicit pair through detail when its serial is absent. With no
// serial, exactly one pair must exist. Local client policy
// requires a model to disambiguate multiple models for one serial. For a single
// matching entry, an explicit model must agree with the catalog.
func (c *Client) SelectDevice(ctx context.Context, d Device) (selected Device, err error) {
	finish := diagnostic.Operation(c.logger, "device_selection")
	defer func() { finish(err) }()
	if d.Serial != "" && !ValidSerial(d.Serial) {
		return Device{}, environmentInputError("--serial", SerialRule)
	}
	devices, complete, duplicates, err := c.devices(ctx)
	if err != nil {
		return Device{}, err
	}
	// The spec's detail workflow supplies both identity fields; serial-only
	// detail lookup is not established. Never bypass a catalog model mismatch.
	if d.Serial != "" && d.Model != "" {
		found := false
		for _, v := range devices {
			found = found || v.Serial() == d.Serial
		}
		if !found {
			if v, detailErr := c.Detail(ctx, d); detailErr == nil {
				return c.selectedDevice(v), nil
			}
		}
	}
	if (!complete || duplicates) && d.Model == "" {
		return Device{}, diagnostic.Wrap("device_selection", "incomplete catalog; name --model to select a received pair", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--model", Rule: "incomplete catalog; name --model to select a received pair"}))
	}
	if len(devices) == 0 {
		return Device{}, diagnostic.Wrap("device_selection", "no devices on account", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--serial", Rule: "select an existing device from the account catalog"}))
	}
	if d.Serial == "" && len(devices) != 1 {
		return Device{}, diagnostic.Wrap("device_selection", "multiple devices on account; set --serial", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--serial", Rule: "select an existing device from the account catalog"}))
	}
	var matches []cloud.Detail
	for _, v := range devices {
		if d.Serial == "" || d.Serial == v.Serial() {
			matches = append(matches, v)
		}
	}
	if len(matches) == 1 && d.Model != "" && d.Model != matches[0].Model() {
		v := matches[0]
		if c.logger != nil {
			c.logger.WarnContext(ctx, "device_selection_refused", "rule", "model_mismatch", "requested_model", displayModel(d.Model), "catalog_model", displayModel(v.Model()))
		}
		// Keep this catalog refusal outside OptionError: startup discards its
		// pending dependency logs for option-validation errors.
		return Device{}, fmt.Errorf("model_mismatch: --model %q differs from catalog model %q: %w", d.Model, v.Model(), cloud.ErrConfig)
	}
	if len(matches) > 1 || (!complete && len(matches) == 1) {
		if d.Model == "" {
			return Device{}, diagnostic.Wrap("device_selection", "serial is ambiguous; name --model to select a catalog pair", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--model", Rule: "serial is ambiguous; name --model to select a catalog pair"}))
		}
		matched := false
		for _, v := range matches {
			if d.Model == v.Model() {
				matches = []cloud.Detail{v}
				matched = true
				break
			}
		}
		if !matched {
			return Device{}, diagnostic.Wrap("device_selection", "device pair not found on account; name --model from the catalog", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--model", Rule: "device pair not found on account; name --model from the catalog"}))
		}
	}
	if len(matches) == 1 {
		return c.selectedDevice(matches[0]), nil
	}
	return Device{}, diagnostic.Wrap("device_selection", "--serial not found on account; check --serial", errors.Join(cloud.ErrConfig, &diagnostic.OptionError{Option: "--serial", Rule: "select an existing device from the account catalog"}))
}

func (c *Client) selectedDevice(v cloud.Detail) Device {
	if c.logger != nil {
		c.logger.Info("device_selected", "model", displayModel(v.Model()), "serial", maskedSerial(v.Serial()))
	}
	return Device{Serial: v.Serial(), Model: v.Model()}
}

// devices requests isContainShared=3 on every page, matching the app 3.3.2
// connect screen (READ), not an observed difference for shared accounts:
// https://github.com/zmerlynn/seestar-spec/blob/main/spec/auth/cloud-auth/http-api.md#device-selection
// It pages the catalog: use positive reply current (otherwise the
// requested page), count wire items including duplicates, and load current+1
// only while the accumulated count is below total. Null items count as empty.
// Local client policy proves completeness only at a positive, exact total;
// no past-the-end request is needed. Unproven catalogs retain received pairs.
// Any duplicate also prevents serial-only selection: repeated wire items can
// satisfy total while other pairs remain unseen. Keep that evidence separate
// from total equality so explicit-model behavior is unchanged.
// Local bounds remain 256 distinct pairs, 16 requests and 100 items/request.
// One timeout covers login and every page. No allocation is made on failure.
// R13c/R13d are inferred guidance; duplicate veto and enumeration budgets are additional local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#device-selection
func (c *Client) devices(ctx context.Context) (devices []cloud.Detail, complete, hasDuplicates bool, err error) {
	finish := diagnostic.Operation(c.logger, "device_list")
	defer func() { finish(err) }()
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	seen := make(map[Device]bool)
	previousTotal := -1
	received, current := 0, 1
	for requests := 1; requests <= 16; requests++ {
		q := url.Values{"current": {strconv.Itoa(current)}, "pageSize": {"100"}, "isContainShared": {"3"}}
		info, b, e := c.exchange(ctx, "/v1/my-device", q, nil, nil)
		page, parseErr := cloud.ParseDevicePageWithLogger(b, c.logger)
		err = responseError(info, b, parseErr, e)
		c.rejected(ctx, "device_list", info, b, cloud.JoinOutcomes(err, parseErr))
		if err != nil {
			return nil, false, false, err
		}
		if page.Current > 0 {
			current = page.Current
		}
		received += page.ItemCount
		duplicates := 0
		for _, v := range page.Devices {
			pair := Device{Serial: v.Serial(), Model: v.Model()}
			if seen[pair] {
				duplicates++
				continue
			}
			if len(devices) == 256 {
				c.rejected(ctx, "device_list", info, b, &cloud.ValidationError{Field: "data.items", Rule: "entry_limit"})
				return nil, false, false, diagnostic.Wrap("device_list", "catalog capacity exceeded", cloud.ErrInvalid)
			}
			seen[pair] = true
			devices = append(devices, v)
		}
		changedTotal := requests > 1 && page.Total != previousTotal
		if c.logger != nil && (page.Total > 256 || changedTotal || duplicates > 0) {
			c.logger.DebugContext(ctx, "cloud_catalog_pagination_oddities",
				"large_total_hint", page.Total > 256, "changed_total_hint", changedTotal,
				"duplicate_items", duplicates)
		}
		if page.Total <= received {
			return devices, page.Total > 0 && page.Total == received, received != len(devices), nil
		}
		// A reply may advance or repeat current independently of request count.
		// Do not overflow current when preparing the next bounded request.
		if requests == 16 || current == math.MaxInt {
			return devices, false, received != len(devices), nil
		}
		previousTotal = page.Total
		current++
	}
	return devices, false, received != len(devices), nil
}

// DisplayName is the public model label and masked serial used for selection.
func (d Device) DisplayName() string {
	return displayModel(d.Model) + " " + maskedSerial(d.Serial)
}

// Model is server-supplied text, so only public product labels enter logs. An
// unknown value still selects correctly but is displayed as [unknown model].
func displayModel(model string) string {
	switch model {
	case "S50 Pro", "S50", "S30", "S30 Pro":
		return model
	}
	return "[unknown model]"
}
func maskedSerial(serial string) string {
	// Never expose a complete short identifier or untrusted suffix characters.
	if len(serial) <= 8 {
		return "***"
	}
	tail := serial[len(serial)-4:]
	for _, c := range tail {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')) {
			return "***"
		}
	}
	return "***" + tail
}
