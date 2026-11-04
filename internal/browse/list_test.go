package browse

import (
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-copy/internal/engine"
	"github.com/zmerlynn/seestar-copy/internal/state"
)

func TestList(t *testing.T) {
	records := map[string]state.Record{
		"MyWorks/M 31/X.fit": {Path: "MyWorks/M 31/X.fit", Size: 200, MTime: t0},
		"MyWorks/M 31/X.jpg": {Path: "MyWorks/M 31/X.jpg", Size: 999, MTime: t0},
		"MyWorks/M 31/sub":   {Path: "MyWorks/M 31/sub", Size: 0, MTime: t0},
	}
	const mt = `"mtime":"2026-11-01T03:10:00.1Z"`
	tests := []struct {
		dest  bool
		query string
		want  string
	}{
		{false, "", `{"path":"","endpoint":"127.0.0.1:4445","statuses":false,"entries":[` +
			`{"path":"MyWorks","type":"dir",` + mt + `}]}`},
		{false, "?path=", `{"path":"","endpoint":"127.0.0.1:4445","statuses":false,"entries":[` +
			`{"path":"MyWorks","type":"dir",` + mt + `}]}`},
		{false, "?path=MyWorks%2FM%2031", `{"path":"MyWorks/M 31","endpoint":"127.0.0.1:4445","statuses":false,"entries":[` +
			`{"path":"MyWorks/M 31/X.fit","type":"file","size":200,` + mt + `,"thumb":"MyWorks/M 31/X_thn.jpg","view":"fits"},` +
			`{"path":"MyWorks/M 31/X.jpg","type":"file","size":4,` + mt + `,"thumb":"MyWorks/M 31/X_thn.jpg","view":"image"},` +
			`{"path":"MyWorks/M 31/X_thn.jpg","type":"file","size":3,` + mt + `,"thumb":"MyWorks/M 31/X_thn.jpg","view":"image"},` +
			`{"path":"MyWorks/M 31/sub","type":"dir",` + mt + `}]}`},
		{true, "?path=MyWorks%2FM%2031", `{"path":"MyWorks/M 31","endpoint":"127.0.0.1:4445","statuses":true,"entries":[` +
			`{"path":"MyWorks/M 31/X.fit","type":"file","size":200,` + mt + `,"status":"copied","thumb":"MyWorks/M 31/X_thn.jpg","view":"fits"},` +
			`{"path":"MyWorks/M 31/X.jpg","type":"file","size":4,` + mt + `,"status":"changed","thumb":"MyWorks/M 31/X_thn.jpg","view":"image"},` +
			`{"path":"MyWorks/M 31/X_thn.jpg","type":"file","size":3,` + mt + `,"status":"new","thumb":"MyWorks/M 31/X_thn.jpg","view":"image"},` +
			`{"path":"MyWorks/M 31/sub","type":"dir",` + mt + `}]}`},
		{true, "?path=MyWorks%2Fodd", `{"path":"MyWorks/odd","endpoint":"127.0.0.1:4445","statuses":true,"entries":[` +
			`{"path":"MyWorks/odd/%2e%2e","type":"file","size":4,` + mt + `,"status":"new"},` +
			`{"path":"MyWorks/odd/a b#%?\u0026+\"\u003cscript\u003e.jpg","type":"file","size":1,` + mt +
			`,"status":"new","thumb":"MyWorks/odd/a b#%?\u0026+\"\u003cscript\u003e.jpg","view":"image"},` +
			"{\"path\":\"MyWorks/odd/e\u0301.fit\",\"type\":\"file\",\"size\":3," + mt + `,"status":"new","view":"fits"},` +
			"{\"path\":\"MyWorks/odd/\u00e9.fit\",\"type\":\"file\",\"size\":3," + mt + `,"status":"new","view":"fits"}]}`},
		{false, "?path=MyWorks%2Flog", `{"path":"MyWorks/log","endpoint":"127.0.0.1:4445","statuses":false,"entries":[` +
			`{"path":"MyWorks/log/Autorun_Log_1.txt","type":"file","size":33,` + mt + `,"view":"text"},` +
			`{"path":"MyWorks/log/b.FITS","type":"file","size":6,` + mt + `,"view":"fits"}]}`},
		{true, "?path=MyWorks%2FM%2031%2Fsub", `{"path":"MyWorks/M 31/sub","endpoint":"127.0.0.1:4445","statuses":true,"entries":[]}`},
	}
	for _, tt := range tests {
		e := newEnv(t, envOptions{dest: tt.dest, records: records})
		w := e.serve("GET", "/api/list"+tt.query)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != tt.want {
			t.Errorf("dest %v, /api/list%s = %d %q\n%s\nwant\n%s", tt.dest, tt.query, w.Code, w.Header().Get("Content-Type"), w.Body, tt.want)
		}
	}
}

func TestListErrors(t *testing.T) {
	e := newEnv(t, envOptions{})
	tests := []struct {
		query string
		code  int
		want  string
	}{
		{"?path=MyWorks%2FM%2031%2FX.fit", 400, `{"error":"not_a_folder","message":"The path is not a folder."}`},
		{"?path=nothing", 404, `{"error":"not_found","message":"Not found on the device."}`},
		{"?path=%ZZ", 400, `{"error":"bad_query","message":"The request's query is malformed."}`},
		{"?path=a%00b", 400, `{"error":"bad_path","message":"The path is not a valid share path."}`},
	}
	for _, tt := range tests {
		w := e.serve("GET", "/api/list"+tt.query)
		if w.Code != tt.code || w.Header().Get("Content-Type") != "application/json" || w.Body.String() != tt.want {
			t.Errorf("/api/list%s = %d %q %s; want %d %s", tt.query, w.Code, w.Header().Get("Content-Type"), w.Body, tt.code, tt.want)
		}
	}
	if e.d.dials() != 1 || e.d.session(0).closed() {
		t.Errorf("a wrong kind of path or a missing one changed the session: %d dials", e.d.dials())
	}
	if e.logs.count("request failed") != 0 {
		t.Errorf("logged a 400 or 404:\n%s", e.logs)
	}

	bad := newEnv(t, envOptions{dest: true, recordsErr: errors.New("state file is torn")})
	w := bad.serve("GET", "/api/list")
	if w.Code != 500 || w.Body.String() != `{"error":"state","message":"state file is torn"}` || bad.d.dials() != 0 {
		t.Errorf("failing state: %d %s, %d dials", w.Code, w.Body, bad.d.dials())
	}
	if bad.logs.count("request failed", "state file is torn") != 1 {
		t.Errorf("logs:\n%s", bad.logs)
	}

	down := newEnv(t, envOptions{dialErr: errors.New("connection refused")})
	w = down.serve("GET", "/api/list")
	if w.Code != 502 || w.Body.String() != `{"error":"device","message":"connection refused"}` {
		t.Errorf("no device: %d %s", w.Code, w.Body)
	}
	if down.logs.count("request failed", "connection refused") != 1 || down.logs.count("connect failed") != 1 {
		t.Errorf("logs:\n%s", down.logs)
	}
}

// TestListLeftOut checks that, with statuses, an entry the selection
// leaves out says so whatever its record, and that every other entry keeps
// its status.
func TestListLeftOut(t *testing.T) {
	records := map[string]state.Record{
		"MyWorks/M 31/X.fit": {Path: "MyWorks/M 31/X.fit", Size: 200, MTime: t0},
		"MyWorks/M 31/X.jpg": {Path: "MyWorks/M 31/X.jpg", Size: 999, MTime: t0},
	}
	extra := fstest.MapFS{
		"MyWorks/M 31/.DS_Store":   {Data: []byte("x"), ModTime: t0},
		"MyWorks/M 31/._.DS_Store": {Data: []byte("x"), ModTime: t0},
	}
	sel := func(excludes, includes []string) engine.Selector {
		var s engine.Selector
		for _, f := range excludes {
			p, err := engine.ParsePattern(f)
			if err != nil {
				t.Fatal(err)
			}
			s.Excludes = append(s.Excludes, p)
		}
		for _, f := range includes {
			p, err := engine.ParsePattern(f)
			if err != nil {
				t.Fatal(err)
			}
			s.Includes = append(s.Includes, p)
		}
		return s
	}
	const m31 = "?path=MyWorks%2FM%2031"
	tests := []struct {
		name     string
		dest     bool
		selector engine.Selector
		query    string
		want     string // path=status pairs, in listing order
	}{
		{"nothing left out", true, engine.Selector{}, m31,
			".DS_Store=new ._.DS_Store=new X.fit=copied X.jpg=changed X_thn.jpg=new sub="},
		{"by name, as the built-in patterns", true, sel(engine.DefaultExcludes, nil), m31,
			".DS_Store=left out ._.DS_Store=left out X.fit=copied X.jpg=changed X_thn.jpg=new sub="},
		{"by path", true, sel([]string{"/MyWorks/M 31/X.jpg", "/X_thn.jpg"}, nil), m31,
			".DS_Store=new ._.DS_Store=new X.fit=copied X.jpg=left out X_thn.jpg=new sub="},
		{"a folder by path", true, sel([]string{"/MyWorks/M 31/sub"}, nil), m31,
			".DS_Store=new ._.DS_Store=new X.fit=copied X.jpg=changed X_thn.jpg=new sub=left out"},
		{"inside a folder left out", true, sel([]string{"m 31"}, nil), m31,
			".DS_Store=left out ._.DS_Store=left out X.fit=left out X.jpg=left out X_thn.jpg=left out sub=left out"},
		{"the folder itself", true, sel([]string{"m 31"}, nil), "?path=MyWorks",
			"M 31=left out log= odd="},
		{"only copy", true, sel(nil, []string{"*.fit"}), m31,
			".DS_Store=left out ._.DS_Store=left out X.fit=copied X.jpg=left out X_thn.jpg=left out sub="},
		{"only copy a folder", true, sel(nil, []string{"/MyWorks/odd"}), "?path=MyWorks",
			"M 31=left out log=left out odd="},
		{"without statuses", false, sel(engine.DefaultExcludes, []string{"*.fit"}), m31,
			".DS_Store= ._.DS_Store= X.fit= X.jpg= X_thn.jpg= sub="},
	}
	for _, tt := range tests {
		e := newEnv(t, envOptions{dest: tt.dest, records: records, extra: extra, selector: tt.selector})
		w := e.serve("GET", "/api/list"+tt.query)
		var got listing
		if err := json.Unmarshal(w.Body.Bytes(), &got); w.Code != http.StatusOK || err != nil {
			t.Fatalf("%s: %d %s", tt.name, w.Code, w.Body)
		}
		var pairs []string
		for _, it := range got.Entries {
			pairs = append(pairs, path.Base(it.Path)+"="+it.Status)
		}
		if s := strings.Join(pairs, " "); s != tt.want || got.Statuses != tt.dest {
			t.Errorf("%s: statuses %t, %s\nwant %s", tt.name, got.Statuses, s, tt.want)
		}
	}
}
