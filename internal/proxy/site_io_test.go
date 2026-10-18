package proxy

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// Checks progress at the next Read call, so latency assertions need no sleeps.
type siteFragmentProgressReader struct {
	chunks  [][]byte
	out     *bytes.Buffer
	want    []byte
	stalled bool
}

func (r *siteFragmentProgressReader) Read(p []byte) (int, error) {
	if !bytes.Equal(r.out.Bytes(), r.want) {
		r.stalled = true
		return 0, errors.New("previous chunk was not forwarded before next read")
	}
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	b := r.chunks[0]
	n := copy(p, b)
	r.want = append(r.want, b[:n]...)
	if n == len(b) {
		r.chunks = r.chunks[1:]
	} else {
		r.chunks[0] = b[n:]
	}
	return n, nil
}

type siteFragmentConn struct {
	io.Reader
	io.Writer
}

func (*siteFragmentConn) Close() error                     { return nil }
func (*siteFragmentConn) CloseWrite() error                { return nil }
func (*siteFragmentConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*siteFragmentConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*siteFragmentConn) SetDeadline(time.Time) error      { return nil }
func (*siteFragmentConn) SetReadDeadline(time.Time) error  { return nil }
func (*siteFragmentConn) SetWriteDeadline(time.Time) error { return nil }

func TestUninspectedImmediateFragments(t *testing.T) {
	var output bytes.Buffer
	chunks := [][]byte{[]byte("partial"), {0, 255, '\n'}, []byte("\r"), []byte("\nnext"), bytes.Repeat([]byte("x"), 65536)}
	want := bytes.Join(chunks, nil)
	src := &siteFragmentProgressReader{chunks: chunks, out: &output}
	a := &siteFragmentConn{Reader: src, Writer: io.Discard}
	b := &siteFragmentConn{Reader: bytes.NewReader(nil), Writer: &output}
	bridge(a, b, false, nil)
	if src.stalled {
		t.Fatal("uninspected path held bytes across a source read")
	}
	if !bytes.Equal(output.Bytes(), want) {
		t.Fatal("uninspected changed or reordered bytes")
	}
}

type siteErrorReader struct {
	b   []byte
	err error
}

func (r *siteErrorReader) Read(p []byte) (int, error) {
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, r.err
}

type siteErrorWriter struct {
	n     int
	err   error
	calls int
}

func (w *siteErrorWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		w.calls++
	}
	return min(w.n, len(b)), w.err
}
func TestSiteEOFAndErrors(t *testing.T) {
	boom := errors.New("injected read/write failure")
	for _, payload := range []string{"partial\r", "lone\n", "\r\n", "nonsetter\r\npartial", string(bytes.Repeat([]byte("z"), MaxSiteLine+17))} {
		for _, readErr := range []error{io.EOF, boom} {
			var out bytes.Buffer
			// Each payload is shorter than the reader buffer except the deliberate overlong case.
			r := io.MultiReader(bytes.NewReader([]byte(payload)), &siteErrorReader{err: readErr})
			err := copySite(&out, r, nil, io.Discard)
			if readErr == io.EOF {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, boom) {
				t.Fatalf("read error lost: %v", err)
			}
			want := ""
			if payload == "\r\n" {
				want = payload
			}
			if out.String() != want {
				t.Fatalf("EOF/error fragment altered, input len=%d output len=%d", len(payload), out.Len())
			}
		}
	}
	for _, payload := range []string{`{"method":"unknown"}` + "\r\n", `{"id":1,"method":"set_setting","params":{"lat":1,"gain":2}}` + "\r\n"} {
		for _, tc := range []struct {
			n    int
			err  error
			want error
		}{{0, nil, io.ErrShortWrite}, {1, nil, io.ErrShortWrite}, {0, boom, boom}, {1, boom, boom}, {2 * MaxSiteLine, boom, boom}} {
			w := &siteErrorWriter{n: tc.n, err: tc.err}
			var logs bytes.Buffer
			err := copySite(w, bytes.NewReader([]byte(payload)), slog.New(slog.NewJSONHandler(&logs, nil)), io.Discard)
			if !errors.Is(err, tc.want) {
				t.Fatalf("write error=%v want=%v", err, tc.want)
			}
			if w.calls > 1 {
				t.Fatalf("continued or logged failed write: calls=%d log bytes=%d", w.calls, logs.Len())
			}
		}
	}
}

func TestSiteBareLFRejected(t *testing.T) {
	setter := []byte(`{"id":4,"method":"set_user_location","params":{"lat":10,"lon":20,"force":true}}`)
	for _, size := range []int{0, MaxSiteLine, MaxSiteLine + 17} {
		bare := append(bytes.Repeat([]byte("x"), size), setter...)
		bare = append(bare, '\n')
		input := append(bytes.Clone(bare), setter...)
		input = append(input, '\r', '\n')
		want := []byte{}
		for _, chunk := range []int{1, 7, MaxSiteLine} {
			var output, replies bytes.Buffer
			if err := copySite(&output, &siteChunkReader{input, chunk}, nil, &replies); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), want) || bytes.Count(replies.Bytes(), []byte("\r\n")) != 2 {
				t.Fatalf("bare LF handling size=%d chunk=%d", size, chunk)
			}
		}
	}
}
