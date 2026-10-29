// Package proxyrun owns CLI port parsing and shared default mappings.
package proxyrun

import (
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"strconv"
	"strings"
)

type Port struct {
	Local  uint16 `json:"local"`
	Device uint16 `json:"device"`
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
	if e != nil || b == 0 {
		return nil, &diagnostic.OptionError{Option: "--port", Rule: "device port must be 1..65535"}
	}
	for _, p := range existing {
		if p.Device == uint16(b) {
			return nil, &diagnostic.OptionError{Option: "--port", Rule: "device ports must be unique"}
		}
		if a != 0 && p.Local == uint16(a) {
			return nil, &diagnostic.OptionError{Option: "--port", Rule: "local ports must be unique"}
		}
	}
	return append(existing, Port{uint16(a), uint16(b)}), nil
}

// MergePorts applies only explicit overrides to the standard service defaults.
func MergePorts(ports []Port) ([]Port, error) {
	defaults := []Port{{4700, 4700}, {4445, 445}, {8080, 80}, {4350, 4350}, {4554, 4554}, {4555, 4555}, {4801, 4801}, {4800, 4800}, {4804, 4804}, {4361, 4361}}
	overrides := ports
	ports = nil
	for _, p := range defaults {
		for _, v := range overrides {
			if p.Device == v.Device {
				p = v
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
			ports = append(ports, v)
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
