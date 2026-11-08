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

// usageExit runs args through the main exit path and decodes its one record:
// text for a file command, JSON for any other.
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
	if len(args) > 0 && fileCommand(args[0]) {
		fields, err := textRecord(logs.String())
		if err != nil || strings.Count(logs.String(), "\n") != 1 {
			t.Fatalf("%q: want one text record, got %q: %v", args, &logs, err)
		}
		get := func(k string) string { s, _ := fields[k].(string); return s }
		record = usageRecord{get("msg"), get("phase"), get("reason"), get("option"), get("rule"), get("help")}
	} else if err := json.Unmarshal(bytes.TrimSpace(logs.Bytes()), &record, json.MatchCaseInsensitiveNames(true)); err != nil {
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
		{[]string{"discovery", "--serial"}, "discovery failed", "--serial", "not a flag of discovery; see " + programName + " proxy --help", "discovery", ""},
		{[]string{"proxy", "--dest", "PRIVATE_VALUE"}, "proxy failed", "--dest", "not a flag of proxy; see " + programName + " sync --help", "proxy", ""},
		{[]string{"proxy", "--older-than=PRIVATE_VALUE"}, "proxy failed", "--older-than", "not a flag of proxy; see " + programName + " find --help", "proxy", ""},
		{[]string{"sync", "--bind", "PRIVATE_VALUE"}, "sync failed", "--bind", "not a flag of sync; see " + programName + " proxy --help", "sync", ""},
		{[]string{"sync", "--zzz-secret=PRIVATE_VALUE"}, "sync failed", "arguments", "unknown flag", "sync", "zzz-secret"},
		{[]string{"sync", "--source", "PRIVATE_VALUE"}, "sync failed", "--source", "removed; sync copies the whole share, so leave folders out with --exclude", "sync", ""},
		{[]string{"find", "--source"}, "find failed", "arguments", "unknown flag", "find", ""},
		{[]string{"sync", "--version"}, "sync failed", "--version", "not a flag of sync; use " + programName + " --version", "sync", ""},
		{[]string{"delete", "--version"}, "delete failed", "--version", "not a flag of delete; use " + programName + " --version", "delete", ""},
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

// The file commands' old --state-dir is refused in every form and names its
// new spelling; proxy's own --state-dir is another folder and still parses.
func TestRetiredStateDir(t *testing.T) {
	files := fileArgs(t)
	for _, name := range fileCommandNames {
		for _, form := range [][]string{{"--state-dir", "PRIVATE_VALUE"}, {"--state-dir=PRIVATE_VALUE"}, {"--state-dir"}} {
			args := append(append([]string{}, files[name]...), form...)
			code, record, logs := usageExit(t, args...)
			if code != 2 || record.Msg != name+" failed" || record.Option != "--state-dir" ||
				record.Rule != "renamed; use --history-dir for the copy history folder" || strings.Contains(logs, "PRIVATE_VALUE") {
				t.Errorf("%q: exit %d record %+v logs %s", args, code, record, logs)
			}
		}
	}
	o, err := parse([]string{"proxy", "--state-dir", "s"}, io.Discard)
	if err != nil || o.stateDir != "s" {
		t.Errorf("proxy --state-dir s: %+v %v", o, err)
	}
}
