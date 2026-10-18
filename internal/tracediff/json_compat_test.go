package tracediff

import (
	"bytes"
	jsonv1 "encoding/json"
	"strings"
	"testing"
)

// V1 is solely the byte comparator for existing report fields and omissions.
func TestDiffJSONV1Bytes(t *testing.T) {
	for _, right := range []string{literal, strings.Replace(literal, `"type":16`, `"type":32`, 1)} {
		got, err := Compare(strings.NewReader(literal), strings.NewReader(right), Options{})
		if err != nil {
			t.Fatal(err)
		}
		var r report
		if err := jsonv1.Unmarshal(got, &r); err != nil {
			t.Fatal(err)
		}
		want, err := jsonv1.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("diff=%s; v1=%s", got, want)
		}
	}
}
