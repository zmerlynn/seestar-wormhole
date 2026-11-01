package ui

import (
	"strconv"
	"testing"
)

func TestSelectedAccountChange(t *testing.T) {
	for _, origin := range []string{"received", "saved"} {
		for _, manual := range []bool{false, true} {
			t.Run(origin+"/manual="+strconv.FormatBool(manual), func(t *testing.T) {
				runPageRegression(t, "selected-account-test.js", "checkSelectedAccountChange("+strconv.Quote(origin)+","+strconv.FormatBool(manual)+")")
			})
		}
	}
}
