package iotc

import (
	"bytes"
	"encoding/base64"
	"github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"
	"sort"
	"strings"
)

// License contains only parsed discovery settings from an edge-verified license.
// The edge performs the raw RSA public operation and digest verification before
// passing the decoded license to New.
type License struct {
	Realm      string
	Mask       [16]byte
	Standalone map[string]string
	// StandaloneOmitted counts unexamined entries beyond the first ten.
	StandaloneOmitted       int
	NebulaDomainFromLicense bool
}

// ParseLicenseJSON applies a 16-KiB local bound and validates discovery fields.
// V2 rejects duplicate names throughout the document as local fail-closed policy;
// this differs from the SDK duplicate override behavior described below.
// The current parser sorts standalone names before selecting ten. V1 specifies
// document order; licenses with more than ten distinct names may select different
// overrides or validate entries the SDK would ignore. This is an incompatibility:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-json
func ParseLicenseJSON(b []byte) (License, error) {
	var l License
	if len(b) == 0 || len(b) > 16384 {
		return l, ErrLimit
	}
	var v struct {
		Realm      *string        `json:"realm"`
		Mask       *string        `json:"keyMask"`
		Standalone jsontext.Value `json:"standalone"`
		Nebula     jsontext.Value `json:"nebulaDomainFromLicense"`
	}
	if json.Unmarshal(b, &v) != nil || v.Realm == nil || len(*v.Realm) == 0 || len(*v.Realm) > 253 || strings.ContainsAny(*v.Realm, "\x00\r\n /:") {
		return l, ErrPacket
	}
	l.Realm = *v.Realm
	// Absent, null and non-boolean Nebula flags mean false.
	_ = json.Unmarshal(v.Nebula, &l.NebulaDomainFromLicense)
	var err error
	l.Standalone, l.StandaloneOmitted, err = licenseOverrides(v.Standalone)
	if err != nil {
		return License{}, err
	}
	if v.Mask != nil {
		m, e := base64.StdEncoding.Strict().DecodeString(*v.Mask)
		if e != nil || len(m) != 16 {
			return License{}, ErrPacket
		}
		copy(l.Mask[:], m)
	}
	return l, nil
}

// licenseOverrides receives syntax-validated JSON within the 16-KiB budget.
// Sorting can change the selected or validated entries beyond ten distinct names:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-json
func licenseOverrides(raw jsontext.Value) (map[string]string, int, error) {
	// The SDK keeps the first ten distinct names in document order with the last
	// value for a repeated name, and does not validate later entries. Sorting
	// here can select different overrides outside that interoperable range.
	var values map[string]jsontext.Value
	// Absent/null means none; non-objects are skipped.
	if len(raw) == 0 || json.Unmarshal(raw, &values) != nil || values == nil {
		return nil, 0, nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	overrides := make(map[string]string)
	for _, name := range names[:min(10, len(names))] {
		var host *string
		if name == "" || len(name) > 128 || json.Unmarshal(values[name], &host) != nil || host == nil || len(*host) > 128 || !domain(*host) {
			return nil, 0, ErrPacket
		}
		overrides[name] = *host
	}
	return overrides, max(0, len(names)-10), nil
}

func domain(s string) bool {
	if len(s) == 0 || len(s) > 253 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '.') {
			return false
		}
	}
	return true
}

// MasterDomain implements client-role discovery, including region cn and standalone.
// Domain suffixes are derived from the shipped binary. Standalone entries override
// construction; dotted realms bypass suffix addition, and only exact cn selects
// the cn suffix. R60 requires honoring the parsed overrides:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/master-legacy.md#master-host-names
func (l License) MasterDomain(region string) (string, error) {
	if region == "" {
		region = "all"
	}
	if !domain(l.Realm) {
		return "", ErrPacket
	}
	if d, ok := l.Standalone[region]; ok {
		if !domain(d) {
			return "", ErrPacket
		}
		return d, nil
	}
	if !domain(region) {
		return "", ErrPacket
	}
	d := region + "-c-master-" + l.Realm
	if !strings.Contains(l.Realm, ".") {
		if region == "cn" {
			d += ".kalay.net.cn"
		} else {
			d += ".iotcplatform.com"
		}
	}
	if !domain(d) {
		return "", ErrPacket
	}
	return d, nil
}

// ParseLicenseEnvelope returns the RSA block and XOR payload for edge validation.
// RSA uses the shipped 1024-bit license public key, with no padding (not master key).
// The envelope separates that RSA block from the XOR-masked JSON payload:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-envelope
func ParseLicenseEnvelope(s string) (block [128]byte, payload []byte, err error) {
	if len(s) > 24000 {
		return block, nil, ErrLimit
	}
	b, e := base64.StdEncoding.Strict().DecodeString(s)
	if e != nil || len(b) <= 132 || len(b) > 16516 || le32(b) != 1 {
		return block, nil, ErrPacket
	}
	copy(block[:], b[4:132])
	return block, b[132:], nil
}

// UnmaskLicense recovers bytes to hash at the edge with SHA256, up to the first
// NUL. Replace digest byte zero by 0x11 and constant-time compare all 32 bytes
// against recovered[:32] BEFORE ParseLicenseJSON. No unchecked wire allocation.
// The envelope digest covers the unmasked bytes before the first NUL:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/sdk-licence.md#licence-envelope
func UnmaskLicense(payload []byte, recovered [128]byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > 16384 {
		return nil, ErrLimit
	}
	b := make([]byte, len(payload))
	for i, v := range payload {
		b[i] = v ^ recovered[i%32]
	}
	if i := bytes.IndexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return b, nil
}
