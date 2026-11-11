package proxyrun

import (
	"slices"
	"testing"
)

func TestMergePortsForwardsAlpaca(t *testing.T) {
	ports, err := MergePorts(nil)
	if err != nil || len(ports) != 11 || ports[10].Local != 32323 || ports[10].Device != 32323 {
		t.Fatalf("defaults = %v, %v", ports, err)
	}
	off, err := MergePorts([]Port{{Local: 0, Device: 32323}})
	if err != nil || len(off) != 10 || slices.ContainsFunc(off, func(p Port) bool { return p.Device == 32323 }) {
		t.Fatalf("0:32323 = %v, %v", off, err)
	}
	moved, err := MergePorts([]Port{{Local: 1234, Device: 32323}})
	if err != nil || len(moved) != 11 || moved[10].Local != 1234 || moved[10].Device != 32323 {
		t.Fatalf("1234:32323 = %v, %v", moved, err)
	}
	if _, err := MergePorts([]Port{{Local: 32323, Device: 9999}}); err == nil {
		t.Fatal("a mapping onto Alpaca's default local port was accepted")
	}
}

func TestAddPortRules(t *testing.T) {
	ports, err := AddPort(nil, Port{Local: 0, Device: 80})
	if err != nil {
		t.Fatal(err)
	}
	if ports, err = AddPort(ports, Port{Local: 0, Device: 445}); err != nil {
		t.Fatal("two disabled mappings share local 0:", err)
	}
	if ports, err = AddPort(ports, Port{Local: 5000, Device: 5000}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []Port{{Local: 1, Device: 0}, {Local: 1, Device: 80}, {Local: 5000, Device: 5001}} {
		if _, err := AddPort(ports, p); err == nil {
			t.Fatalf("%v was accepted", p)
		}
	}
	full := make([]Port, 128)
	for i := range full {
		full[i] = Port{Local: uint16(10000 + i), Device: uint16(10000 + i)}
	}
	if _, err := AddPort(full, Port{Local: 1, Device: 1}); err == nil {
		t.Fatal("a 129th mapping was accepted")
	}
	if _, err := ParsePort(ports, "6000:0"); err == nil {
		t.Fatal("device port 0 was accepted")
	}
}
