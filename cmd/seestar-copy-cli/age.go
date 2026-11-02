package main

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
)

// ageValue is the value of an --older-than flag: a Go duration, such as 36h
// or 90m, or a whole number of days, such as 7d. The two do not mix: 1d12h
// is refused. A negative value parses; the command refuses it.
type ageValue time.Duration

const day = 24 * time.Hour

func (a *ageValue) Set(s string) error {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.ParseInt(n, 10, 64)
		if err != nil || days > math.MaxInt64/int64(day) || days < math.MinInt64/int64(day) {
			return errors.New("not a whole number of days")
		}
		*a = ageValue(time.Duration(days) * day)
		return nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*a = ageValue(d)
	return nil
}

// String prints a whole number of days as days, so a default of 7d shows as
// 7d in the help, and zero as 0, which the help leaves out.
func (a *ageValue) String() string {
	d := time.Duration(*a)
	switch {
	case d == 0:
		return "0"
	case d%day == 0:
		return strconv.FormatInt(int64(d/day), 10) + "d"
	}
	return d.String()
}

func (a *ageValue) Type() string { return "age" }
