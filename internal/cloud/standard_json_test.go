package cloud

import (
	"strings"
	"testing"
)

func TestStandardJSONUnknownFields(t *testing.T) {
	for name, parse := range responseParsers() {
		t.Run(name, func(t *testing.T) {
			body := strings.Replace(responseFixtures()[name], `"code":200`, `"code":200,"future":{"a":1,"b":2},"nested":`+strings.Repeat(`[`, 20)+`0`+strings.Repeat(`]`, 20), 1)
			if err := parse([]byte(body)); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		})
	}
}
