package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSyncHelp(t *testing.T) {
	code, stdout, stderr := invoke(t, "sync", "--help")
	if code != exitOK || stderr != "" {
		t.Fatalf("code=%d stderr=%q, want 0 and no stderr", code, stderr)
	}
	for _, want := range []string{
		"--endpoint string", "--dest string", "--exclude stringArray", "--no-default-excludes", "--include stringArray",
		"Excludes always win, whatever the order:\n--include x --exclude x selects nothing",
		// The built-in patterns, as the literal flags they equal.
		"\n  --exclude '.*'\n  --exclude '*.tmp'\n  --exclude 'Thumbs.db'\n  --exclude 'desktop.ini'\n" +
			"  --exclude '$RECYCLE.BIN'\n  --exclude 'System Volume Information'\n  --exclude '/log'\n" +
			"  --exclude '/tmp_stitch'\n  --exclude '/batch_stack_tmp'\n",
		"--interval duration", "(default 5m0s)", "--once", "--dry-run", "(default 2m0s)", "(default 10m0s)",
		"files copied at once (1-64) (default 4)", "--state-dir string",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("sync help lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, "Third-party license information") {
		t.Error("the license pointer belongs to the root help only")
	}
}

func TestEndpoint(t *testing.T) {
	for _, endpoint := range []string{"h", "h:", ":445", "h:0", "h:65536", "h:smb", "h:-1", "h:+445", "::1:445", "h:445:1", "[::1]"} {
		code, _, stderr := invoke(t, "sync", "--endpoint", endpoint, "--dest", "d", "--state-dir", "s")
		if code != exitUsage || !strings.Contains(stderr, `option=--endpoint rule="use host:port, such as 127.0.0.1:4445"`) {
			t.Errorf("%q: code=%d stderr=%q, want 2 naming --endpoint", endpoint, code, stderr)
		}
	}
	for _, endpoint := range []string{"127.0.0.1:4445", "[::1]:445", "seestar.local:445", "h:1", "h:65535"} {
		code, _, stderr := invoke(t, slices.Concat([]string{"sync", "--endpoint", endpoint}, scratch(t))...)
		if code != exitFailure || !strings.Contains(stderr, "connect to "+endpoint+": "+errNoDevice.Error()) {
			t.Errorf("%q: code=%d stderr=%q, want accepted and the stubbed connect failure", endpoint, code, stderr)
		}
	}
}

// A bad --exclude or --include is a usage error naming that flag before
// anything is dialed, also with --no-default-excludes; the value is not
// echoed.
func TestPatternUsageErrors(t *testing.T) {
	const (
		parts   = `rule="use a name, or a path from the share root, with no empty, . or .. parts"`
		slash   = `rule="omit the trailing /; a pattern matches files and folders"`
		class   = `rule="[ and \\ are not supported; use * and ?, and / between folders"`
		globstr = `rule="** is not supported; * matches within one name"`
	)
	dev := newDevice()
	dev.use(t)
	for _, tc := range []struct{ pattern, want string }{
		{"", parts}, {"/", parts}, {"//", slash}, {"//x", parts}, {"a//b", parts}, {"./a", parts}, {"a/.", parts},
		{"..", parts}, {"/../log", parts}, {".", parts}, {"log/", slash}, {"/log/", slash},
		{"M[42]", class}, {`MyWorks\x`, class}, {"**", globstr}, {"MyWorks/**/x", globstr}, {"a**", globstr},
	} {
		for _, flag := range []string{"--exclude", "--include"} {
			for _, extra := range [][]string{nil, {"--no-default-excludes"}} {
				args := slices.Concat([]string{"sync", "--endpoint", "h:445"}, scratch(t), extra, []string{flag, "ok", flag, tc.pattern})
				code, _, stderr := invoke(t, args...)
				if want := "option=" + flag + " " + tc.want; code != exitUsage || !strings.Contains(stderr, want) {
					t.Errorf("%s %q %q: code=%d stderr=%q, want 2 with %q", flag, tc.pattern, extra, code, stderr, want)
				}
				if len(tc.pattern) > 2 && strings.Contains(stderr, tc.pattern) {
					t.Errorf("%s %q: the value is echoed: %q", flag, tc.pattern, stderr)
				}
			}
		}
	}
	if dials, _ := dev.counts(); dials != 0 {
		t.Errorf("%d sessions dialed, want none", dials)
	}
}

func TestSyncFlags(t *testing.T) {
	for _, tc := range []struct {
		args     []string
		excludes []string
		includes []string
	}{
		{nil, nil, nil},
		{[]string{"--exclude", "MyWorks"}, []string{"MyWorks"}, nil},
		{[]string{"--exclude", "b", "--no-default-excludes", "--exclude", "a,c"}, []string{"b", "a,c"}, nil},
		{[]string{"--include", "b", "--exclude", "x", "--include", "a,c"}, []string{"x"}, []string{"b", "a,c"}},
	} {
		cmd := NewSync(programName)
		if err := cmd.ParseFlags(tc.args); err != nil {
			t.Fatal(err)
		}
		f := cmd.Flags()
		excludes, _ := f.GetStringArray("exclude")
		if !slices.Equal(excludes, tc.excludes) {
			t.Errorf("%q: excludes %q, want %q", tc.args, excludes, tc.excludes)
		}
		if includes, _ := f.GetStringArray("include"); !slices.Equal(includes, tc.includes) {
			t.Errorf("%q: includes %q, want %q", tc.args, includes, tc.includes)
		}
		if len(tc.args) > 0 {
			continue
		}
		for name, want := range map[string]time.Duration{"interval": 5 * time.Minute, "ready-age": 2 * time.Minute, "first-pass-age": 10 * time.Minute} {
			if got, _ := f.GetDuration(name); got != want {
				t.Errorf("--%s default %v, want %v", name, got, want)
			}
		}
		if got, _ := f.GetInt("workers"); got != 4 {
			t.Errorf("--workers default %d, want 4", got)
		}
		for name, want := range map[string]string{"endpoint": "", "dest": "", "state-dir": ""} {
			if got, _ := f.GetString(name); got != want {
				t.Errorf("--%s default %q, want %q", name, got, want)
			}
		}
		if got, _ := f.GetBool("once"); got {
			t.Error("--once is on by default")
		}
		if got, _ := f.GetBool("dry-run"); got {
			t.Error("--dry-run is on by default")
		}
		if got, _ := f.GetBool("no-default-excludes"); got {
			t.Error("--no-default-excludes is on by default")
		}
	}
}

func TestValidDefaults(t *testing.T) {
	o := syncOptions{endpoint: "h:445", dest: "d", interval: 5 * time.Minute,
		readyAge: 0, firstPassAge: 0, workers: 1}
	if err := o.validate(); err != nil {
		t.Errorf("zero ages and one worker: %v", err)
	}
}

// The bounds are literal: the help and the error both promise 1 to 64.
func TestWorkerBounds(t *testing.T) {
	for _, tc := range []struct {
		workers string
		code    int
		want    string
	}{
		{"1", exitFailure, errNoDevice.Error()},
		{"64", exitFailure, errNoDevice.Error()},
		{"0", exitUsage, `option=--workers rule="must be 1 to 64"`},
		{"65", exitUsage, `option=--workers rule="must be 1 to 64"`},
	} {
		code, stdout, stderr := invoke(t, slices.Concat([]string{"sync", "--endpoint", "h:445", "--workers", tc.workers}, scratch(t))...)
		if code != tc.code || stdout != "" || !strings.Contains(stderr, tc.want) {
			t.Errorf("--workers %s: code=%d stdout=%q stderr=%q, want %d with %q", tc.workers, code, stdout, stderr, tc.code, tc.want)
		}
	}
}

func TestStateDir(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", base)
	t.Setenv("HOME", base)
	t.Setenv("AppData", base)
	want, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	want = filepath.Join(want, "seestar-copy")
	got, err := resolveStateDir("", false)
	if err != nil || got != want {
		t.Errorf("default state dir %q, %v, want %q", got, err, want)
	}
	got, err = resolveStateDir("x", true)
	if err != nil || got != "x" {
		t.Errorf("explicit state dir %q, %v, want x", got, err)
	}
}
