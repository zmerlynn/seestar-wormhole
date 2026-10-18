package proxy

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

type siteProgressReader struct {
	first, rest []byte
	out         *bytes.Buffer
}

func (r *siteProgressReader) Read(b []byte) (int, error) {
	if len(r.first) > 0 {
		n := copy(b, r.first)
		r.first = r.first[n:]
		return n, nil
	}
	if r.out.Len() != 0 {
		return 0, errors.New("overlong prefix reached the device before next read")
	}
	if len(r.rest) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

func TestSiteOverlongDropsBeforeNextRead(t *testing.T) {
	var out bytes.Buffer
	first := bytes.Repeat([]byte(" "), MaxSiteLine)
	rest := append(bytes.Clone(siteFixture(t)[0]), '\r', '\n')
	r := &siteProgressReader{bytes.Clone(first), bytes.Clone(rest), &out}
	if err := copySite(&out, r, nil, io.Discard); err != nil {
		t.Fatal(err)
	}
	if out.Len() != 0 {
		t.Fatal("overlong frame reached device")
	}
}

type siteShortWriter struct{}

func (siteShortWriter) Write(b []byte) (int, error) { return len(b) / 2, nil }

func TestSiteShortWrite(t *testing.T) {
	if err := copySite(siteShortWriter{}, strings.NewReader(`{"method":"unknown"}`+"\r\n"), nil, io.Discard); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short write error=%v want=%v", err, io.ErrShortWrite)
	}
}

type siteChunkReader struct {
	b     []byte
	chunk int
}

func (r *siteChunkReader) Read(b []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.b[:min(r.chunk, len(r.b))])
	r.b = r.b[n:]
	return n, nil
}

func TestSiteSplitAndOverlong(t *testing.T) {
	line := append(bytes.Clone(siteFixture(t)[0]), '\r', '\n')
	wantLine := []byte{}
	for _, size := range []int{len(line), MaxSiteLine, MaxSiteLine + 1, 3 * MaxSiteLine} {
		padded := append(bytes.Repeat([]byte(" "), size-len(line)), line...)
		want := append(bytes.Clone(wantLine), wantLine...)
		if size > MaxSiteLine {
			want = bytes.Clone(wantLine)
		}
		input := append(bytes.Clone(padded), line...)
		// Empty separators pass through; incomplete EOF fragments are rejected.
		input = append(input, []byte("\npartial\r")...)
		want = append(want, '\n')
		for _, chunk := range []int{1, 2, 7, 4096, MaxSiteLine, MaxSiteLine + 1} {
			var out bytes.Buffer
			if err := copySite(&out, &siteChunkReader{input, chunk}, nil, io.Discard); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out.Bytes(), want) {
				t.Fatalf("size=%d chunk=%d output mismatch", size, chunk)
			}
		}
	}
}

func FuzzSiteProtection(f *testing.F) {
	for _, b := range siteFixture(f) {
		f.Add(append(bytes.Clone(b), '\r', '\n'), uint16(7))
	}
	f.Add([]byte("broken\r\n"), uint16(1))
	for _, raw := range []string{
		`{"method":"pi_set_time","params":{"time_zone":"America/Los_Angeles","year":2001}}`,
		`{"method":"set_user_location","params":[{"lat":"10","lon":"20"},null]}`,
		`{"method":"set_setting","params":{"lat":"10","time_zone":"America/Los_Angeles"}}`,
		`{"method":"pi_set_time","params":null}`,
		`{"method":"pi_set_time","method":null}`,
		`{"method":"get_setting","future":{"x":1,"x":2}}`,
		`{"method":"get_setting","metho\u0064":"pi_set_time"}`,
		`[{"method":"pi_set_time","params":{}}]`,
	} {
		f.Add([]byte(raw+"\r\n"), uint16(1))
	}
	f.Fuzz(func(t *testing.T, b []byte, chunk uint16) {
		if len(b) > 4*MaxSiteLine {
			t.Skip()
		}
		var a, c, ra, rc bytes.Buffer
		if err := copySite(&a, bytes.NewReader(b), nil, &ra); err != nil {
			t.Fatal(err)
		}
		if err := copySite(&c, &siteChunkReader{b, int(chunk) + 1}, nil, &rc); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(a.Bytes(), c.Bytes()) || !bytes.Equal(ra.Bytes(), rc.Bytes()) {
			t.Fatal("output depends on read splitting")
		}
	})
}
