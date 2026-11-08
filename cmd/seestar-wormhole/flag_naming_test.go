package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"os"
	"strings"
	"testing"
)

type usageRecord struct{ Msg, Phase, Reason, Option, Rule, Help string }

// usageExit runs args through the main exit path and decodes its one JSON record.
func usageExit(t *testing.T, args ...string) (int, usageRecord, string) {
	t.Helper()
	var out, logs bytes.Buffer
	code := mainExitCodeWithPlatform(args, &out, &logs,
		func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) { return ctx, func() {} },
		func() (io.Writer, func()) { return &logs, func() {} }, false)
	if out.Len() != 0 {
		t.Errorf("%q: stdout %q", args, &out)
	}
	var record usageRecord
	if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
		t.Fatalf("%q: want one JSON record, got %q: %v", args, &logs, err)
	}
	return code, record, logs.String()
}

// A flag is named only when the program defines that name somewhere in its
// tree; any other name could be a typed secret and is reported as arguments.
func TestFlagNaming(t *testing.T) {
	for _, tc := range []struct {
		args              []string
		msg, option, rule string
		help, secret      string
	}{
		{[]string{"proxy", "--zzz-secret=PRIVATE_VALUE"}, "proxy failed", "arguments", "unknown flag", "proxy", "zzz-secret"},
		{[]string{"proxy", "-z"}, "proxy failed", "arguments", "unknown flag", "proxy", ""},
		{[]string{"licenses", "--idle=PRIVATE_VALUE"}, "licenses failed", "--idle", "not a flag of licenses; see " + programName + " proxy --help", "licenses", ""},
		{[]string{"discovery", "-p", "1:1"}, "discovery failed", "-p", "not a flag of discovery; see " + programName + " proxy --help", "discovery", ""},
		{[]string{"discovery", "--state-dir"}, "discovery failed", "--state-dir", "not a flag of discovery; see " + programName + " proxy --help", "discovery", ""},
		{[]string{"proxy", "--advertise-serial", "PRIVATE_VALUE"}, "proxy failed", "--advertise-serial", "not a flag of proxy; see " + programName + " discovery --help", "proxy", ""},
		{[]string{"licenses", "--version"}, "licenses failed", "--version", "not a flag of licenses; use " + programName + " --version", "licenses", ""},
		{[]string{"--idle", "2m"}, programName + " failed", "--idle", "not a flag of " + programName + "; see " + programName + " proxy --help", "", ""},
		{[]string{"--zzz-secret"}, programName + " failed", "arguments", "unknown flag", "", "zzz-secret"},
	} {
		code, record, logs := usageExit(t, tc.args...)
		help := programName + " " + tc.help + " --help"
		if tc.help == "" {
			help = programName + " --help"
		}
		if code != 2 || record.Msg != tc.msg || record.Phase != "arguments" || record.Reason != "invalid_option" ||
			record.Option != tc.option || record.Rule != tc.rule || record.Help != help {
			t.Errorf("%q: exit %d record %+v; want %s option=%s rule=%q help=%q", tc.args, code, record, tc.msg, tc.option, tc.rule, help)
		}
		if strings.Contains(logs, "PRIVATE_VALUE") || tc.secret != "" && strings.Contains(logs, tc.secret) {
			t.Errorf("%q: record echoes input: %s", tc.args, logs)
		}
	}
}

// A root usage error is the program's own, not proxy's.
func TestRootUsageErrorLabel(t *testing.T) {
	code, record, logs := usageExit(t, "nonesuch")
	if code != 2 || record.Msg != programName+" failed" || record.Help != programName+" --help" || strings.Contains(logs, "proxy") {
		t.Errorf("exit %d record %+v logs %s", code, record, logs)
	}
}
