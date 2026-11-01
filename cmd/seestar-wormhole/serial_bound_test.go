package main

import (
	"bytes"
	"errors"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
	"io"
	"strings"
	"testing"
)

func TestSerialBoundCLI(t *testing.T) {
	for _, value := range []string{strings.Repeat("s", 63), strings.Repeat("s", 64)} {
		if _, err := parse([]string{"proxy", "--serial=" + value, "--version"}, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []string{strings.Repeat("s", 65), strings.Repeat("\u202e", 11), "s ", "s\x7f", "s\x80"} {
		var out, logs bytes.Buffer
		err := run(t.Context(), []string{"proxy", "--serial=" + value, "--version"}, &out, &logs)
		var option *diagnostic.OptionError
		const rule = "use a device serial of 1-64 printable ASCII bytes (0x21-0x7E; no spaces)"
		if !errors.As(err, &option) || option.Option != "--serial" || option.Rule != rule || out.Len() != 0 {
			t.Fatalf("wrong refusal: %v", err)
		}
	}
	var help bytes.Buffer
	parse([]string{"proxy", "--help"}, &help)
	if !strings.Contains(help.String(), "1-64 printable ASCII bytes (0x21-0x7E; no spaces)") {
		t.Fatal("serial help omits exact bound")
	}
}
