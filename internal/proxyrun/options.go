// Package proxyrun owns CLI port parsing and shared default mappings.
package proxyrun

import (
	"encoding/json/jsontext"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"strconv"
	"strings"
)

type Port struct {
	Local   uint16         `json:"local"`
	Device  uint16         `json:"device"`
	Unknown jsontext.Value `json:",embed"`
}

func ParsePort(existing []Port, s string) ([]Port, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "use local:device ports"}
	}
	if len(existing) >= 128 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "at most 128 mappings are allowed"}
	}
	a, e := strconv.ParseUint(parts[0], 10, 16)
	if e != nil {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "local port must be 0..65535"}
	}
	b, e := strconv.ParseUint(parts[1], 10, 16)
	if e != nil {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "device port must be 1..65535"}
	}
	return AddPort(existing, Port{Local: uint16(a), Device: uint16(b)})
}

// AddPort appends p to existing under the rules of -p: at most 128
// mappings, each device port 1..65535 given once, and each nonzero local
// port used once.
func AddPort(existing []Port, p Port) ([]Port, error) {
	if len(existing) >= 128 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "at most 128 mappings are allowed"}
	}
	if p.Device == 0 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "device port must be 1..65535"}
	}
	for _, v := range existing {
		if v.Device == p.Device {
			return nil, &diagnostic.OptionError{Option: "--port", Rule: "device ports must be unique"}
		}
		if p.Local != 0 && v.Local == p.Local {
			return nil, &diagnostic.OptionError{Option: "--port", Rule: "local ports must be unique"}
		}
	}
	return append(existing, p), nil
}

// MergePorts applies only explicit overrides to the standard service defaults.
func MergePorts(ports []Port) ([]Port, error) {
	defaults := []Port{{Local: 4700, Device: 4700}, {Local: 4445, Device: 445}, {Local: 8080, Device: 80}, {Local: 4350, Device: 4350}, {Local: 4554, Device: 4554}, {Local: 4555, Device: 4555}, {Local: 4801, Device: 4801}, {Local: 4800, Device: 4800}, {Local: 4804, Device: 4804}, {Local: 4361, Device: 4361}}
	overrides := ports
	ports = nil
	for _, p := range defaults {
		for _, v := range overrides {
			if p.Device == v.Device {
				p = Port{Local: v.Local, Device: v.Device}
				break
			}
		}
		if p.Local != 0 {
			ports = append(ports, p)
		}
	}
	for _, v := range overrides {
		known := false
		for _, p := range defaults {
			if p.Device == v.Device {
				known = true
				break
			}
		}
		if !known && v.Local != 0 {
			ports = append(ports, Port{Local: v.Local, Device: v.Device})
		}
	}
	if len(ports) > 128 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "at most 128 mappings are allowed"}
	}
	locals := make(map[uint16]bool)
	for _, p := range ports {
		if locals[p.Local] {
			return nil, &diagnostic.OptionError{Option: "--port", Rule: "local ports must be unique"}
		}
		locals[p.Local] = true
	}
	return ports, nil
}
