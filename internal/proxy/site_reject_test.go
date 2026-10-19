package proxy

import (
	"bytes"
	"errors"
	"github.com/go-json-experiment/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestSiteRejectedStreamSplitsAndLog(t *testing.T) {
	valid := `{"id":1,"method":"set_setting","params":[{"lat":"10","lon":"20","force":false},null]}` + "\r\n"
	want := valid
	bad := `{"id":"private id","method":"pi_set_time","params":null}` + "\r\n"
	input := valid + bad + strings.Repeat(" ", MaxSiteLine) + bad + valid + bad[:len(bad)-2]
	for _, chunk := range []int{1, 2, 7, 4096, MaxSiteLine + 1, len(input)} {
		var output, reply, logs bytes.Buffer
		logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// Timestamps can coincidentally contain the synthetic coordinate values.
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		}}))
		if err := copySite(&output, &siteChunkReader{[]byte(input), chunk}, logger, &reply); err != nil {
			t.Fatal(err)
		}
		if output.String() != want+want || bytes.Count(reply.Bytes(), []byte("\r\n")) != 3 {
			t.Fatalf("stream mismatch chunk=%d", chunk)
		}
		for _, line := range bytes.Split(logs.Bytes(), []byte("\n")) {
			var event struct{ Msg, Level string }
			if json.Unmarshal(line, &event, json.MatchCaseInsensitiveNames(true)) == nil && event.Msg == "site_reject" && event.Level != "WARN" {
				t.Fatal("rejection not WARN")
			}
		}
		for _, secret := range []string{"private id", "-12.5", "45.25", "Pacific/Kiritimati"} {
			if strings.Contains(logs.String(), secret) {
				t.Fatal("rejection log includes values")
			}
		}
	}
	if err := copySite(io.Discard, strings.NewReader(bad), nil, siteShortWriter{}); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("local reply short write=%v", err)
	}
}

func TestSiteDeviceFragmentsImmediate(t *testing.T) {
	var out bytes.Buffer
	parts := [][]byte{[]byte("partial"), []byte("\r"), []byte("\nnext"), bytes.Repeat([]byte("x"), 2*MaxSiteLine), []byte("\r\n")}
	want := bytes.Join(parts, nil)
	reader := &siteFragmentProgressReader{chunks: parts, out: &out}
	writer := &siteClientWriter{writer: &out}
	if err := writer.copyDevice(reader); err != nil || reader.stalled || !bytes.Equal(out.Bytes(), want) {
		t.Fatalf("device fragments changed/held: %v", err)
	}
}

func TestSiteLocalReplyAfterDeviceEOF(t *testing.T) {
	for _, device := range []string{"", "complete\r\n", `{"id":1,"result":`} {
		t.Run(device, func(t *testing.T) {
			var out bytes.Buffer
			writer := &siteClientWriter{writer: &out}
			if err := writer.copyDevice(strings.NewReader(device)); err != nil {
				t.Fatal(err)
			}
			if n, err := writer.Write([]byte("local-error\r\n")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("retired local reply: n=%d err=%v want n=0 err=ErrClosedPipe", n, err)
			}
			for _, tc := range []struct{ frame, rule string }{
				{`{"id":"private-id","method":"pi_set_time"}` + "\r\n", "site_protected"},
				{`{"id":"private-id","method":"foo"}` + "\n", "incomplete_frame"},
			} {
				var forwarded, logs bytes.Buffer
				const ordinary = "{\"method\":\"get_setting\"}\r\n"
				logger := slog.New(slog.NewJSONHandler(&logs, nil))
				if err := copySite(&forwarded, strings.NewReader(tc.frame+ordinary), logger, writer); err != nil {
					t.Fatal(err)
				}
				if forwarded.String() != ordinary || out.String() != device {
					t.Fatalf("forwarded=%q downstream=%q", forwarded.String(), out.String())
				}
				records := bytes.Split(bytes.TrimSpace(logs.Bytes()), []byte("\n"))
				var record struct{ Msg, Level, Reason, Cause string }
				if len(records) != 2 || json.Unmarshal(records[len(records)-1], &record, json.MatchCaseInsensitiveNames(true)) != nil || record.Msg != "site_reply_failed" || record.Level != "WARN" || record.Reason != tc.rule || record.Cause != "device_closed" {
					t.Fatalf("log=%s want unsent WARN rule=%s", logs.Bytes(), tc.rule)
				}
				if strings.Contains(logs.String(), "private-id") {
					t.Fatalf("ID leaked: %s", logs.Bytes())
				}
			}
		})
	}
}

type siteBlockingWriter struct {
	started chan struct{}
	release chan struct{}
}

func (w siteBlockingWriter) Write(b []byte) (int, error) {
	close(w.started)
	<-w.release
	return len(b), nil
}

func TestSiteDeviceReadErrorWithBlockedReply(t *testing.T) {
	blocked := siteBlockingWriter{started: make(chan struct{}), release: make(chan struct{})}
	writer := &siteClientWriter{writer: blocked}
	reply := make(chan error, 1)
	go func() { reply <- writeSite(writer, []byte("local-error\r\n")) }()
	<-blocked.started
	boom := errors.New("device read failed")
	downstream := make(chan error, 1)
	go func() { downstream <- writer.copyDevice(&siteErrorReader{err: boom}) }()
	var readErr error
	returned := false
	select {
	case readErr = <-downstream:
		returned = true
	case <-time.After(5 * time.Second):
		t.Error("device read failure waited for blocked reply; bridge cannot stop I/O")
	}
	close(blocked.release)
	if err := <-reply; err != nil {
		t.Fatal(err)
	}
	if !returned {
		readErr = <-downstream
	}
	if !errors.Is(readErr, boom) {
		t.Fatalf("device read error=%v want=%v", readErr, boom)
	}
}

// A local error waits for a fragmented device response to finish. synctest
// proves the ordering without time sleeps or relying on TCP packet grouping.
func TestSiteErrorAtDeviceFrameBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		read, write := io.Pipe()
		defer read.Close()
		defer write.Close()
		var out bytes.Buffer
		writer := &siteClientWriter{writer: &out}
		downstream := make(chan error, 1)
		go func() { downstream <- writer.copyDevice(read) }()
		prefix := strings.Repeat("x", MaxSiteLine+3)
		if _, err := io.WriteString(write, prefix); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		// The downstream write lock is held across its next read.
		reply := make(chan error, 1)
		started := make(chan struct{})
		go func() { close(started); reply <- writeSite(writer, []byte("local-error\r\n")) }()
		<-started
		select {
		case <-reply:
			t.Fatal("local error interleaved in response")
		default:
		}
		if _, err := io.WriteString(write, "\r\n"); err != nil {
			t.Fatal(err)
		}
		if err := <-reply; err != nil {
			t.Fatal(err)
		}
		write.Close()
		if err := <-downstream; err != nil {
			t.Fatal(err)
		}
		if out.String() != prefix+"\r\nlocal-error\r\n" {
			t.Fatal("device frame or local error corrupted")
		}
	})
}
