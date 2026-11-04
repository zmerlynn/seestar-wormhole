package main

import (
	"testing"
	"time"
)

func TestAgeValue(t *testing.T) {
	for _, tc := range []struct {
		in     string
		want   time.Duration
		string string
	}{
		{"0", 0, "0"},
		{"0d", 0, "0"},
		{"7d", 7 * 24 * time.Hour, "7d"},
		{"+2d", 48 * time.Hour, "2d"},
		{"-1d", -24 * time.Hour, "-1d"},
		{"168h", 7 * 24 * time.Hour, "7d"},
		{"36h", 36 * time.Hour, "36h0m0s"},
		{"90m", 90 * time.Minute, "1h30m0s"},
		{"1.5h", 90 * time.Minute, "1h30m0s"},
		{"500ms", 500 * time.Millisecond, "500ms"},
		{"106751d", 106751 * 24 * time.Hour, "106751d"},
	} {
		var a ageValue
		if err := a.Set(tc.in); err != nil {
			t.Errorf("%q: %v", tc.in, err)
			continue
		}
		if time.Duration(a) != tc.want || a.String() != tc.string {
			t.Errorf("%q: got %v (%s), want %v (%s)", tc.in, time.Duration(a), a.String(), tc.want, tc.string)
		}
	}
	for _, in := range []string{"", "d", "7", "1d12h", "1.5d", "7D", "7 d", "d7", "0x10d", "106752d", "7days", "1w", "1e3d"} {
		a := ageValue(time.Hour)
		if err := a.Set(in); err == nil {
			t.Errorf("%q: accepted as %v", in, time.Duration(a))
		}
	}
}
