package main

import (
	"io"
	"testing"
	"time"
)

func TestStatsIntervalFlag(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want time.Duration
		bad  bool
	}{
		{nil, time.Second, false}, {[]string{"--stats-interval", "0"}, 0, false}, {[]string{"--stats-interval", "250ms"}, 250 * time.Millisecond, false}, {[]string{"--stats-interval", "-1s"}, 0, true}, {[]string{"--stats-interval", "-1ns"}, 0, true},
	} {
		o, err := parse(append([]string{"proxy"}, tc.args...), io.Discard)
		if (err != nil) != tc.bad || !tc.bad && o.statsInterval != tc.want {
			t.Fatalf("args=%v stats=%v err=%v want=%v bad=%v", tc.args, o.statsInterval, err, tc.want, tc.bad)
		}
	}
}
