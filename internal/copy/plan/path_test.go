package plan

import (
	"io/fs"
	"path"
	"testing"
)

// pathTests are also the fuzz seeds. want is the reason for each setting of
// the Windows rules; "" means the path is accepted.
var pathTests = []struct {
	path               string
	wantPosix, wantWin string
}{
	// Accepted everywhere.
	{"MyWorks/M 31/Light_M 31_10.0s_IRCUT_20261029-033119.fit", "", ""},
	{"Autorun/Dark/x.fit", "", ""},
	{"a", "", ""},
	{"a/.hidden", "", ""},
	{"a/..b/c", "", ""},
	{"a/b..", "", "name \"b..\" ends in a dot, which Windows cannot store"},
	{".seestar-copy-tmp/a", "", ""},
	{"a/.seestar-copy-tmp", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp/b", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcde.tmp", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0.tmp", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg.tmp", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp.fit", "", ""},
	{"a/x.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp", "", ""},
	{"a/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tm", "", ""},
	{"a/CONSOLE.fit", "", ""},
	{"a/COM10", "", ""},
	{"a/LPT", "", ""},
	{"a/xCON", "", ""},
	{"a/NULL.txt", "", ""},
	{"a/COMa", "", ""},
	{"a/COM⁴", "", ""},
	{"a/ CON", "", ""},
	{"Ünïcødé/星雲.fit", "", ""},
	{"a\x7f", "", ""},
	{"a\u0085", "", ""},
	{"a/b\u009f", "", ""},

	// Rejected everywhere.
	{"", "empty path", "empty path"},
	{"/a", "absolute path", "absolute path"},
	{"/", "absolute path", "absolute path"},
	{`a\b`, "backslash in path", "backslash in path"},
	{`\a`, "backslash in path", "backslash in path"},
	{"C:/a", "path starts with a drive letter", "path starts with a drive letter"},
	{"z:a", "path starts with a drive letter", "path starts with a drive letter"},
	{"a\x00b", "control character U+0000 in path", "control character U+0000 in path"},
	{"\x00", "control character U+0000 in path", "control character U+0000 in path"},
	{"a\xffb", "path is not valid UTF-8", "path is not valid UTF-8"},
	{"a//b", "empty path element", "empty path element"},
	{"a/", "empty path element", "empty path element"},
	{".", `path element "."`, `path element "."`},
	{"..", `path element ".."`, `path element ".."`},
	{"./a", `path element "."`, `path element "."`},
	{"a/.", `path element "."`, `path element "."`},
	{"a/../b", `path element ".."`, `path element ".."`},
	{"../a", `path element ".."`, `path element ".."`},
	{".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp", "name \".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp\" is reserved for temporary files", "name \".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp\" is reserved for temporary files"},
	{"MyWorks/M 31/.seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp", "name \".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp\" is reserved for temporary files", "name \".seestar-copy-0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef.tmp\" is reserved for temporary files"},
	{"a/.Seestar-Copy-0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF.TMP", "name \".Seestar-Copy-0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF.TMP\" is reserved for temporary files", "name \".Seestar-Copy-0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF.TMP\" is reserved for temporary files"},

	// Rejected only on Windows.
	{"a\x01b", "", "control character U+0001 is not allowed on Windows"},
	{"a/b\x1f", "", "control character U+001F is not allowed on Windows"},
	{"a\nb", "", "control character U+000A is not allowed on Windows"},
	{"\tb:c", "", "control character U+0009 is not allowed on Windows"},
	{"ab:c", "", `character ':' is not allowed on Windows`},
	{"a/b*", "", `character '*' is not allowed on Windows`},
	{"a/b?", "", `character '?' is not allowed on Windows`},
	{`a/"b"`, "", `character '"' is not allowed on Windows`},
	{"a/<b", "", `character '<' is not allowed on Windows`},
	{"a/b>", "", `character '>' is not allowed on Windows`},
	{"a/b|c", "", `character '|' is not allowed on Windows`},
	{"a./b", "", "name \"a.\" ends in a dot, which Windows cannot store"},
	{"a/b.", "", "name \"b.\" ends in a dot, which Windows cannot store"},
	{"a /b", "", "name \"a \" ends in a space, which Windows cannot store"},
	{"a/b ", "", "name \"b \" ends in a space, which Windows cannot store"},
	{"CON", "", "name \"CON\" is a reserved device name on Windows"},
	{"a/con", "", "name \"con\" is a reserved device name on Windows"},
	{"a/Prn.txt", "", "name \"Prn.txt\" is a reserved device name on Windows"},
	{"aux.tar.gz/b", "", "name \"aux.tar.gz\" is a reserved device name on Windows"},
	{"a/nUl", "", "name \"nUl\" is a reserved device name on Windows"},
	{"a/CON .txt", "", "name \"CON .txt\" is a reserved device name on Windows"},
	{"a/conin$", "", "name \"conin$\" is a reserved device name on Windows"},
	{"a/CONOUT$.log", "", "name \"CONOUT$.log\" is a reserved device name on Windows"},
	{"a/COM0", "", "name \"COM0\" is a reserved device name on Windows"},
	{"a/com9.fit", "", "name \"com9.fit\" is a reserved device name on Windows"},
	{"a/LPT0", "", "name \"LPT0\" is a reserved device name on Windows"},
	{"a/lpt5.jpg", "", "name \"lpt5.jpg\" is a reserved device name on Windows"},
	{"a/COM¹", "", "name \"COM¹\" is a reserved device name on Windows"},
	{"a/lpt².txt", "", "name \"lpt².txt\" is a reserved device name on Windows"},
	{"a/Com³", "", "name \"Com³\" is a reserved device name on Windows"},
}

func TestCheckPath(t *testing.T) {
	for _, tc := range pathTests {
		if got := checkPath(tc.path, false); got != tc.wantPosix {
			t.Errorf("checkPath(%q, false) = %q, want %q", tc.path, got, tc.wantPosix)
		}
		if got := checkPath(tc.path, true); got != tc.wantWin {
			t.Errorf("checkPath(%q, true) = %q, want %q", tc.path, got, tc.wantWin)
		}
	}
}

// FuzzCheckPath checks that the validator never panics, that every path it
// accepts is already clean and valid for io/fs, and that the Windows rules
// only ever add rejections.
func FuzzCheckPath(f *testing.F) {
	for _, tc := range pathTests {
		f.Add(tc.path, false)
		f.Add(tc.path, true)
	}
	f.Fuzz(func(t *testing.T, p string, windows bool) {
		if checkPath(p, windows) != "" {
			return
		}
		if c := path.Clean(p); c != p {
			t.Errorf("checkPath accepted %q, which cleans to %q", p, c)
		}
		if !fs.ValidPath(p) {
			t.Errorf("checkPath accepted %q, which io/fs rejects", p)
		}
		if windows {
			if r := checkPath(p, false); r != "" {
				t.Errorf("checkPath accepted %q for Windows but not elsewhere: %q", p, r)
			}
		}
	})
}

func TestIsTempName(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	for _, tc := range []struct {
		name string
		want bool
	}{
		{".seestar-copy-" + hex + ".tmp", true},
		{".seestar-copy-" + "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff" + ".tmp", true},
		// Only the exact form is ours; other spellings are refused by
		// checkPath but never taken for a temporary file.
		{".Seestar-copy-" + hex + ".tmp", false},
		{".seestar-copy-" + hex + ".TMP", false},
		{".seestar-copy-" + hex[:63] + "A.tmp", false},
		{".seestar-copy-" + hex[:63] + "g.tmp", false},
		{".seestar-copy-" + hex[:63] + ".tmp", false},
		{".seestar-copy-" + hex + "0.tmp", false},
		{"seestar-copy-" + hex + ".tmp", false},
		{"..seestar-copy-" + hex[1:] + ".tmp", false},
		{".seestar-copy-" + hex + ".tmp.fit", false},
		{".seestar-copy-" + hex + "xtmp", false},
		{".seestar-copy-tmp", false},
		{"", false},
	} {
		if got := IsTempName(tc.name); got != tc.want {
			t.Errorf("IsTempName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTempName(t *testing.T) {
	// The SHA-256 of "MyWorks/M 31/a.fit", worked out independently.
	const want = ".seestar-copy-56d43d7223c53ec6db390faceddf947830ba59b0bd75bc9a91bb783a458233b1.tmp"
	for _, p := range []string{"MyWorks/M 31/a.fit", "a", "Ünïcødé/星雲.fit"} {
		got := TempName(p)
		if !IsTempName(got) {
			t.Errorf("TempName(%q) = %q, which IsTempName rejects", p, got)
		}
		if reason := checkPath("a/"+got, false); reason == "" {
			t.Errorf("checkPath accepts the temporary name %q", got)
		}
	}
	if got := TempName("MyWorks/M 31/a.fit"); got != want {
		t.Errorf("TempName = %q, want %q", got, want)
	}
	if TempName("a") == TempName("b") {
		t.Error("TempName gives two paths the same name")
	}
}
