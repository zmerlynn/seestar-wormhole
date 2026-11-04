package remote

// These tests run Dial's go-smb2 client against a scripted SMB2 peer over
// net.Pipe, inside a synctest bubble: no socket, no Samba. The peer decodes
// requests and encodes responses with go-smb2's own x/wire types, so the
// tests can see exactly what the client asks of the share.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/binary"
	"errors"
	"io"
	"io/fs"
	"net"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
	"github.com/hirochachacha/go-smb2/v2/x/wire"
)

// NTSTATUS codes the peer answers with ([MS-ERREF] 2.3.1).
const (
	statusAccessDenied       = 0xC0000022
	statusObjectNameNotFound = 0xC0000034
	statusEndOfFile          = 0xC0000011
	statusBadNetworkName     = 0xC00000CC
	statusNotSupported       = 0xC00000BB
)

// wireEndpoint is what the tests dial; the pipe dialer ignores it.
const wireEndpoint = "fixture.invalid:14450"

// testCredentials is an authentication that is complete at once, so the
// peer needs no NTLM. TestDialerConfig checks the credentials Dial uses.
type testCredentials struct{}

func (testCredentials) NewInitiator(context.Context, string) (auth.Initiator, error) {
	return testCredentials{}, nil
}
func (testCredentials) OID() asn1.ObjectIdentifier {
	return asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 311, 2, 2, 10}
}
func (testCredentials) InitSecContext() ([]byte, error)         { return nil, nil }
func (testCredentials) AcceptSecContext([]byte) ([]byte, error) { return nil, nil }
func (testCredentials) GetMIC([]byte) ([]byte, error)           { return nil, nil }
func (testCredentials) VerifyMIC([]byte, []byte) error          { return nil }
func (testCredentials) Complete() bool                          { return true }
func (testCredentials) SessionKey() []byte                      { return nil }

type pipeDialer struct{ conn net.Conn }

func (d pipeDialer) Dial(context.Context, string) (smb2.Transport, error) {
	return smb2.NewTransport(d.conn), nil
}

// peer is a scripted SMB2 server for one connection. It serves one file,
// refuses deny with access denied, and reports every other name as missing.
type peer struct {
	t      *testing.T
	client net.Conn
	conn   net.Conn
	file   string // SMB spelling, with backslashes
	data   []byte
	deny   string
	// notDir is a name CREATE refuses as not a directory.
	notDir string
	// tree, if not zero, is the status TREE_CONNECT fails with.
	tree uint32
	// hold lists commands the peer reads but never answers, from the
	// first request on. holdNow adds to it later, once Dial has set up the
	// session. mu guards it after startPeer.
	hold []wire.Command
	held chan struct{}
	once sync.Once
	done chan struct{}

	mu       sync.Mutex
	requests []wire.PacketCodec
}

func startPeer(t *testing.T, p *peer) *peer {
	p.t = t
	p.client, p.conn = net.Pipe()
	p.held = make(chan struct{})
	p.done = make(chan struct{})
	go p.serve()
	return p
}

// stop closes the connection, which ends anything still pending, and waits
// for the peer to finish.
func (p *peer) stop() {
	_ = p.conn.Close()
	<-p.done
}

// dial runs Dial's configuration over the pipe.
func (p *peer) dial(ctx context.Context) (*SMB, error) {
	d := newDialer()
	d.Credentials = testCredentials{}
	d.TransportDialer = pipeDialer{p.client}
	return dial(ctx, wireEndpoint, d)
}

// mustDial dials and fails the test if Dial fails, or if it has not
// returned within a second of bubble time. A Dial the peer holds then fails
// the test instead of deadlocking the bubble; the test's deferred stop ends
// the held Dial.
func (p *peer) mustDial(t *testing.T, ctx context.Context) *SMB {
	t.Helper()
	type dialed struct {
		s   *SMB
		err error
	}
	done := make(chan dialed, 1)
	go func() {
		s, err := p.dial(ctx)
		done <- dialed{s, err}
	}()
	select {
	case d := <-done:
		if d.err != nil {
			t.Fatalf("Dial: %v", d.err)
		}
		return d.s
	case <-time.After(time.Second):
		t.Fatal("Dial still blocked after 1s")
		return nil
	}
}

// holdNow makes the peer hold cmds from the next request on. Tests of a
// session's calls arm their holds after Dial, so a request Dial itself
// sends is never held by mistake.
func (p *peer) holdNow(cmds ...wire.Command) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.hold = append(p.hold, cmds...)
}

// awaitHeld waits for the peer to hold a request, failing the test if none
// arrives within a second (of bubble time, inside synctest).
func (p *peer) awaitHeld(t *testing.T) {
	t.Helper()
	select {
	case <-p.held:
	case <-time.After(time.Second):
		t.Fatal("no request held after 1s")
	}
}

// logoffs counts the LOGOFF requests the peer has seen.
func (p *peer) logoffs() int {
	n := 0
	for _, req := range p.seen() {
		if req.Command() == wire.SMB2_LOGOFF {
			n++
		}
	}
	return n
}

func (p *peer) seen() []wire.PacketCodec {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.requests)
}

func (p *peer) serve() {
	defer close(p.done)
	for {
		var prefix [4]byte
		if _, err := io.ReadFull(p.conn, prefix[:]); err != nil {
			return
		}
		size := binary.BigEndian.Uint32(prefix[:])
		if size > 1<<20 {
			p.t.Errorf("request frame of %d bytes", size)
			return
		}
		frame := make([]byte, size)
		if _, err := io.ReadFull(p.conn, frame); err != nil {
			return
		}
		// A compound holds several requests chained by NextCommand; a
		// related request fails with the status of the one before it.
		var status uint32
		for len(frame) > 0 {
			req := wire.PacketCodec(frame)
			if req.IsInvalidRequest() {
				p.t.Errorf("invalid request frame % x", frame)
				return
			}
			next := int(req.NextCommand())
			if next == 0 || next > len(frame) {
				next = len(frame)
			}
			req = req[:next]
			frame = frame[next:]
			if req.Flags()&wire.SMB2_FLAGS_RELATED_OPERATIONS == 0 || status == 0 {
				status = 0
			}
			var ok bool
			if status, ok = p.handle(req, status); !ok {
				return
			}
		}
	}
}

// handle answers one request, or holds it. prior is the failure of the
// request this one is related to, if any; handle returns its own status.
func (p *peer) handle(req wire.PacketCodec, prior uint32) (uint32, bool) {
	cmd := req.Command()
	p.mu.Lock()
	p.requests = append(p.requests, slices.Clone(req))
	hold := slices.Contains(p.hold, cmd)
	p.mu.Unlock()
	if hold {
		p.once.Do(func() { close(p.held) })
		return 0, true
	}
	var res wire.Packet
	status := prior
	if status == 0 {
		res, status = p.respond(req)
	}
	if status != 0 {
		e := &wire.ErrorResponse{CommandCode: cmd}
		e.Status = status
		res = e
	}
	if res == nil {
		return status, true // CANCEL has no response.
	}
	res.SetMessageId(req.MessageId())
	res.SetSessionId(1)
	res.SetTreeId(req.TreeId())
	res.SetFlags(wire.SMB2_FLAGS_SERVER_TO_REDIR)
	res.SetCreditResponse(64)
	frame := make([]byte, 4+res.Size())
	binary.BigEndian.PutUint32(frame, uint32(res.Size()))
	res.Encode(frame[4:])
	_, err := p.conn.Write(frame)
	return status, err == nil
}

func (p *peer) respond(req wire.PacketCodec) (wire.Packet, uint32) {
	switch req.Command() {
	case wire.SMB2_NEGOTIATE:
		return &wire.NegotiateResponse{DialectRevision: wire.SMB210, SecurityMode: wire.SMB2_NEGOTIATE_SIGNING_ENABLED,
			MaxTransactSize: 65536, MaxReadSize: 65536, MaxWriteSize: 65536}, 0
	case wire.SMB2_SESSION_SETUP:
		// SPNEGO accept-completed for the test mechanism ([RFC 4178] 4.2.2).
		token, err := asn1.Marshal(struct {
			State asn1.Enumerated       `asn1:"explicit,tag:0"`
			Mech  asn1.ObjectIdentifier `asn1:"explicit,tag:1"`
		}{0, testCredentials{}.OID()})
		if err == nil {
			token, err = asn1.Marshal(asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 1, IsCompound: true, Bytes: token})
		}
		if err != nil {
			p.t.Error(err)
		}
		return &wire.SessionSetupResponse{SessionFlags: wire.SMB2_SESSION_FLAG_IS_GUEST, SecurityBuffer: token}, 0
	case wire.SMB2_TREE_CONNECT:
		if p.tree != 0 {
			return nil, p.tree
		}
		return &wire.TreeConnectResponse{ShareType: wire.SMB2_SHARE_TYPE_DISK, MaximalAccess: 0x001f01ff}, 0
	case wire.SMB2_CREATE:
		c := wire.CreateRequestDecoder(req.Body())
		if c.IsInvalid() {
			p.t.Errorf("invalid CREATE % x", req.Body())
			return nil, statusNotSupported
		}
		switch c.Name() {
		case p.file:
			return &wire.CreateResponse{EndofFile: int64(len(p.data)), FileAttributes: wire.FILE_ATTRIBUTE_NORMAL,
				FileId: wire.FileId{Persistent: [8]byte{1}, Volatile: [8]byte{2}}}, 0
		case p.deny:
			return nil, statusAccessDenied
		}
		if p.notDir != "" && c.Name() == p.notDir {
			return nil, statusNotADirectory
		}
		return nil, statusObjectNameNotFound
	case wire.SMB2_READ:
		r := wire.ReadRequestDecoder(req.Body())
		if r.IsInvalid() {
			p.t.Errorf("invalid READ % x", req.Body())
			return nil, statusNotSupported
		}
		off := r.Offset()
		if off >= uint64(len(p.data)) {
			return nil, statusEndOfFile
		}
		end := min(off+uint64(r.Length()), uint64(len(p.data)))
		return &wire.ReadResponse{Data: p.data[off:end]}, 0
	case wire.SMB2_CLOSE:
		return &wire.CloseResponse{}, 0
	case wire.SMB2_TREE_DISCONNECT:
		return &wire.TreeDisconnectResponse{}, 0
	case wire.SMB2_LOGOFF:
		return &wire.LogoffResponse{}, 0
	case wire.SMB2_CANCEL:
		return nil, 0
	}
	// Anything else is reported by the test from the request log.
	return nil, statusNotSupported
}

// await returns what done delivers, failing the test if that takes longer
// than within. Time is the bubble's, so the bound costs nothing.
func await(t *testing.T, done <-chan error, within time.Duration, what string) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("%s still blocked after %v", what, within)
		return nil
	}
}

func TestDialerConfig(t *testing.T) {
	d := newDialer()
	if c, ok := d.Credentials.(auth.NTLMCredential); !ok || !reflect.DeepEqual(c, auth.NTLMCredential{User: "guest"}) {
		t.Errorf("Credentials = %#v, want NTLM guest with an empty password", d.Credentials)
	}
	if !d.DisableAAPLExtension {
		t.Error("the AAPL extension is enabled")
	}
	if d.TransportDialer != nil {
		t.Errorf("TransportDialer = %#v, want go-smb2's TCP default", d.TransportDialer)
	}
}

// writeAccess is every access right that could change a file or its
// metadata ([MS-SMB2] 2.2.13.1).
const writeAccess = wire.FILE_WRITE_DATA | wire.FILE_APPEND_DATA | wire.FILE_WRITE_EA | wire.FILE_DELETE_CHILD |
	wire.FILE_WRITE_ATTRIBUTES | wire.DELETE | wire.WRITE_DAC | wire.WRITE_OWNER | wire.MAXIMUM_ALLOWED |
	wire.GENERIC_ALL | wire.GENERIC_WRITE

// readOnlyCommands are the only requests a read-only session may send.
var readOnlyCommands = []wire.Command{
	wire.SMB2_NEGOTIATE, wire.SMB2_SESSION_SETUP, wire.SMB2_LOGOFF, wire.SMB2_TREE_CONNECT, wire.SMB2_TREE_DISCONNECT,
	wire.SMB2_CREATE, wire.SMB2_CLOSE, wire.SMB2_READ, wire.SMB2_QUERY_DIRECTORY, wire.SMB2_QUERY_INFO, wire.SMB2_CANCEL,
}

// TestSMBRequestsAreReadOnly drives every Session call and checks each
// request the share receives.
func TestSMBRequestsAreReadOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		data := content(200000)
		p := startPeer(t, &peer{file: `MyWorks\M 31\a.fit`, data: data, deny: `MyWorks\locked.fit`})
		defer p.stop()
		ctx := t.Context()
		s := p.mustDial(t, ctx)

		f, err := s.Open(ctx, "MyWorks/M 31/a.fit")
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.New()
		n, err := io.Copy(h, f)
		if want := sha256.Sum256(data); err != nil || n != int64(len(data)) || !bytes.Equal(h.Sum(nil), want[:]) {
			t.Errorf("copy = %d, %v; want %d bytes with the file's SHA-256", n, err, len(data))
		}
		if err := f.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		f, err = s.Open(ctx, "MyWorks/M 31/a.fit")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := io.ReadAll(struct{ io.Reader }{f}); err != nil || !bytes.Equal(got, data) {
			t.Errorf("ReadAll = %d bytes, %v; want %d", len(got), err, len(data))
		}
		if n, err := f.Seek(10, io.SeekStart); n != 10 || err != nil {
			t.Errorf("Seek = %d, %v; want 10", n, err)
		}
		// The peer does not answer QUERY_INFO; only the request matters.
		_, _ = f.Stat()
		_, _ = f.Seek(0, io.SeekEnd)
		_ = f.Close()
		if _, err := s.ReadDir(ctx, "MyWorks"); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("ReadDir = %v, want fs.ErrNotExist", err)
		}
		// The peer reports every name but its file, the root too, as missing.
		if _, err := s.Walk(ctx, nil, nil); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Walk = %v, want fs.ErrNotExist", err)
		}
		if _, err := s.Open(ctx, "MyWorks/locked.fit"); !errors.Is(err, fs.ErrPermission) {
			t.Errorf("Open of a denied file = %v, want fs.ErrPermission", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}

		var trees, creates, logoffs int
		for _, req := range p.seen() {
			cmd := req.Command()
			if !slices.Contains(readOnlyCommands, cmd) {
				t.Errorf("sent %v", cmd)
			}
			switch cmd {
			case wire.SMB2_TREE_CONNECT:
				trees++
				if path := wire.TreeConnectRequestDecoder(req.Body()).Path(); path != `\\fixture.invalid\EMMC Images` {
					t.Errorf("TREE_CONNECT %q, want the EMMC Images share", path)
				}
			case wire.SMB2_CREATE:
				creates++
				c := wire.CreateRequestDecoder(req.Body())
				if c.CreateDisposition() != wire.FILE_OPEN {
					t.Errorf("CREATE %q: disposition %#x, want FILE_OPEN", c.Name(), c.CreateDisposition())
				}
				if a := c.DesiredAccess(); a&writeAccess != 0 {
					t.Errorf("CREATE %q: desired access %#x has write rights %#x", c.Name(), a, a&writeAccess)
				}
				if c.CreateOptions()&wire.FILE_DELETE_ON_CLOSE != 0 {
					t.Errorf("CREATE %q: delete on close", c.Name())
				}
				if c.Name() == p.file && c.DesiredAccess()&(wire.GENERIC_READ|wire.FILE_READ_DATA) == 0 {
					t.Errorf("CREATE %q: desired access %#x cannot read data", c.Name(), c.DesiredAccess())
				}
				// Every create context's name sits inside the list, so
				// a byte search finds an AAPL one.
				if ctxs := c.Contexts(); bytes.Contains(ctxs, []byte("AAPL")) {
					t.Errorf("CREATE %q carries an AAPL context", c.Name())
				}
			case wire.SMB2_LOGOFF:
				logoffs++
			}
		}
		if trees != 1 || creates < 4 || logoffs != 1 {
			t.Errorf("saw %d TREE_CONNECT, %d CREATE, %d LOGOFF; want 1, at least 4, 1", trees, creates, logoffs)
		}
	})
}

// TestSMBErrorsNameEndpointAndPath checks that errors say where they
// happened and still match what they wrap.
func TestSMBErrorsNameEndpointAndPath(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{tree: statusBadNetworkName})
		defer p.stop()
		_, err := p.dial(t.Context())
		var pe *fs.PathError
		if err == nil || !errors.As(err, &pe) || !strings.Contains(err.Error(), wireEndpoint) || !strings.Contains(err.Error(), ShareName) {
			t.Errorf("failed mount = %v, want a PathError naming %s and %q", err, wireEndpoint, ShareName)
		}
	})
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{file: `MyWorks\a.fit`, deny: `MyWorks\locked.fit`})
		defer p.stop()
		ctx := t.Context()
		s := p.mustDial(t, ctx)
		defer s.Close()
		var pe *fs.PathError
		_, err := s.Open(ctx, "MyWorks/locked.fit")
		if !errors.Is(err, fs.ErrPermission) || !errors.As(err, &pe) || !strings.Contains(err.Error(), wireEndpoint) || !strings.Contains(err.Error(), `"MyWorks/locked.fit"`) {
			t.Errorf("denied Open = %v, want fs.ErrPermission naming %s and the path", err, wireEndpoint)
		}
		_, err = s.Walk(ctx, nil, nil)
		if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), wireEndpoint) {
			t.Errorf("Walk of a missing root = %v, want fs.ErrNotExist naming %s", err, wireEndpoint)
		}
	})
}

// TestDialCanceledDuringMount cancels Dial while the server sits on
// TREE_CONNECT.
func TestDialCanceledDuringMount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{hold: []wire.Command{wire.SMB2_TREE_CONNECT}})
		defer p.stop()
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() {
			s, err := p.dial(ctx)
			if err == nil {
				_ = s.Close()
			}
			done <- err
		}()
		p.awaitHeld(t)
		start := time.Now()
		cancel()
		err := await(t, done, time.Second, "Dial")
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "mount") {
			t.Errorf("Dial = %v, want a canceled mount", err)
		}
		if d := time.Since(start); d != 0 {
			t.Errorf("Dial took %v after the cancel, want no wait", d)
		}
	})
}

// gatedClose holds the client's Close until release, before it closes the
// connection. go-smb2's receiver meanwhile stays in its read of the pipe,
// which a synctest bubble counts as blocked, so bubble time moves on while
// Close is held.
type gatedClose struct {
	net.Conn
	entered, release chan struct{}
}

func (c gatedClose) Close() error {
	close(c.entered)
	<-c.release
	return c.Conn.Close()
}

// gateClose puts a gatedClose on the client's end of the connection. The
// returned release may be called more than once; tests defer it, so a
// failing test does not leave Close held.
func (p *peer) gateClose() (gatedClose, func()) {
	conn := gatedClose{Conn: p.client, entered: make(chan struct{}), release: make(chan struct{})}
	p.client = conn
	var once sync.Once
	return conn, func() { once.Do(func() { close(conn.release) }) }
}

// awaitClose waits for the client to enter Close, failing the test if that
// takes more than a second of bubble time.
func (c gatedClose) awaitClose(t *testing.T, what string) {
	t.Helper()
	select {
	case <-c.entered:
	case <-time.After(time.Second):
		t.Fatalf("%s did not close the connection", what)
	}
}

// failHeldTreeConnect stops holding requests and answers the held
// TREE_CONNECT with status.
func (p *peer) failHeldTreeConnect(t *testing.T, status uint32) {
	t.Helper()
	var held wire.PacketCodec
	p.mu.Lock()
	p.hold = nil
	for _, req := range p.requests {
		if req.Command() == wire.SMB2_TREE_CONNECT {
			held = req
		}
	}
	p.mu.Unlock()
	if held == nil {
		t.Fatal("no TREE_CONNECT is held")
	}
	if _, ok := p.handle(held, status); !ok {
		t.Fatal("could not answer the held TREE_CONNECT")
	}
}

// TestDialJoinsMountAbort checks that a Dial canceled during Mount returns
// only once the abort of its session has finished. The abort's Close is
// held, and the peer then fails the held TREE_CONNECT, so Mount returns
// while the abort is still running. Dial must wait through 10s of bubble
// time, longer than any bounded wait for the abort would plausibly be.
func TestDialJoinsMountAbort(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{hold: []wire.Command{wire.SMB2_TREE_CONNECT}})
		defer p.stop()
		conn, release := p.gateClose()
		defer release()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			s, err := p.dial(ctx)
			if err == nil {
				_ = s.Close()
			}
			done <- err
		}()
		p.awaitHeld(t)
		cancel()
		conn.awaitClose(t, "the canceled Mount")
		p.failHeldTreeConnect(t, statusBadNetworkName)
		select {
		case err := <-done:
			t.Fatalf("Dial returned %v before its abort finished", err)
		case <-time.After(10 * time.Second):
		}
		release()
		if err := await(t, done, time.Second, "Dial"); !errors.Is(err, context.Canceled) {
			t.Errorf("Dial = %v, want context.Canceled", err)
		}
	})
}

// TestFailedMountLogsOff checks that a Dial whose Mount fails logs off and
// closes the connection before it returns, not when the peer goes away.
// Transport Close is held, and Dial must not return while it is. The hold
// lasts 1s of bubble time: go-smb2 forces a session's Close after 5s, and
// that forced close would wait on a mutex, which stalls the bubble.
func TestFailedMountLogsOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{tree: statusBadNetworkName})
		defer p.stop()
		conn, release := p.gateClose()
		defer release()
		done := make(chan error, 1)
		go func() {
			s, err := p.dial(t.Context())
			if err == nil {
				_ = s.Close()
				err = errors.New("Dial succeeded with a failing TREE_CONNECT")
			}
			done <- err
		}()
		conn.awaitClose(t, "the failed Mount")
		select {
		case err := <-done:
			t.Fatalf("Dial returned %v before closing the connection", err)
		case <-time.After(time.Second):
		}
		release()
		if err := await(t, done, time.Second, "Dial"); err == nil || !strings.Contains(err.Error(), "mount") {
			t.Errorf("Dial = %v, want a failed mount", err)
		}
		// The peer finishes when the client closes its end.
		select {
		case <-p.done:
		case <-time.After(time.Second):
			t.Error("the connection is still open after the failed Dial")
		}
		if n := p.logoffs(); n != 1 {
			t.Errorf("sent %d LOGOFF after the failed Mount, want 1", n)
		}
	})
}

// watchContext counts the AfterFunc watchers registered on it and the ones
// stopped again. Value hides the embedded cancel context, so that
// context.AfterFunc goes through watchContext's AfterFunc.
type watchContext struct {
	context.Context
	added, stopped atomic.Int32
}

func (c *watchContext) Value(any) any { return nil }

func (c *watchContext) AfterFunc(f func()) func() bool {
	c.added.Add(1)
	stop := context.AfterFunc(c.Context, f)
	return func() bool {
		ok := stop()
		if ok {
			c.stopped.Add(1)
		}
		return ok
	}
}

// TestCloseStopsWatcher checks that a session keeps one watcher on its
// Dial context and that Close removes it, so sessions on a long-lived
// context are not retained until that context ends.
func TestCloseStopsWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{})
		defer p.stop()
		ctx := &watchContext{Context: t.Context()}
		s := p.mustDial(t, ctx)
		if added, stopped := ctx.added.Load(), ctx.stopped.Load(); added-stopped != 1 {
			t.Errorf("after Dial, %d of %d watchers are left, want 1", added-stopped, added)
		}
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if added, stopped := ctx.added.Load(), ctx.stopped.Load(); added-stopped != 0 {
			t.Errorf("after Close, %d of %d watchers are left, want none", added-stopped, added)
		}
	})
}

// TestPendingCallsEnd holds a call's request and checks that ending the
// session, by its Dial context or by Close, ends the call and logs off once.
// The call's own context stays live, so only the session's end can release
// it.
func TestPendingCallsEnd(t *testing.T) {
	type call struct {
		hold wire.Command
		run  func(*SMB) error
	}
	read := func(s *SMB, pipelined bool) error {
		f, err := s.Open(context.Background(), "MyWorks/a.fit")
		if err != nil {
			return err
		}
		defer f.Close()
		if pipelined {
			_, err = f.WriteTo(io.Discard)
		} else {
			_, err = f.Read(make([]byte, 5))
		}
		return err
	}
	calls := map[string]call{
		"Open":    {wire.SMB2_CREATE, func(s *SMB) error { _, err := s.Open(context.Background(), "MyWorks/a.fit"); return err }},
		"Walk":    {wire.SMB2_CREATE, func(s *SMB) error { _, err := s.Walk(context.Background(), nil, nil); return err }},
		"Read":    {wire.SMB2_READ, func(s *SMB) error { return read(s, false) }},
		"WriteTo": {wire.SMB2_READ, func(s *SMB) error { return read(s, true) }},
	}
	newPeer := func(t *testing.T) *peer {
		return startPeer(t, &peer{file: `MyWorks\a.fit`, data: []byte("hello")})
	}
	for name, c := range calls {
		t.Run(name+"/DialContext", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := newPeer(t)
				defer p.stop()
				ctx, cancel := context.WithCancel(t.Context())
				s := p.mustDial(t, ctx)
				p.holdNow(c.hold)
				done := make(chan error, 1)
				go func() { done <- c.run(s) }()
				p.awaitHeld(t)
				cancel()
				if err := await(t, done, time.Second, name); err == nil {
					t.Errorf("%s succeeded after the session ended", name)
				}
				closed := make(chan error, 1)
				go func() { closed <- s.Close() }()
				_ = await(t, closed, time.Second, "Close")
				if n := p.logoffs(); n != 1 {
					t.Errorf("sent %d LOGOFF after the Dial context ended, want 1", n)
				}
			})
		})
		// A server that also ignores LOGOFF holds Close for go-smb2's
		// log-off timeout, and no longer.
		for _, hold := range [][]wire.Command{{c.hold}, {c.hold, wire.SMB2_LOGOFF}} {
			t.Run(name+"/Close/"+hold[len(hold)-1].String(), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					p := newPeer(t)
					defer p.stop()
					s := p.mustDial(t, context.Background())
					p.holdNow(hold...)
					done := make(chan error, 1)
					go func() { done <- c.run(s) }()
					p.awaitHeld(t)
					closed := make(chan error, 1)
					go func() { closed <- s.Close() }()
					_ = await(t, closed, 6*time.Second, "Close")
					if err := await(t, done, time.Second, name); err == nil {
						t.Errorf("%s succeeded after Close", name)
					}
					if n := p.logoffs(); n != 1 {
						t.Errorf("sent %d LOGOFF on Close, want 1", n)
					}
				})
			})
		}
	}
}

// TestSMBRemoverRefusesInvalidPaths checks that SMBRemover refuses every
// path Open refuses before any request reaches the share: no CREATE goes
// out for any of them.
func TestSMBRemoverRefusesInvalidPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{file: `a\b`})
		defer p.stop()
		ctx := t.Context()
		r := &SMBRemover{p.mustDial(t, ctx)}
		defer r.Close()
		for _, path := range invalidRemovePaths {
			if err := r.RemoveFile(ctx, Entry{Path: path, Kind: Regular}); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("RemoveFile(%q) = %v, want fs.ErrInvalid", path, err)
			}
			if err := r.RemoveDir(ctx, path); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("RemoveDir(%q) = %v, want fs.ErrInvalid", path, err)
			}
		}
		for _, req := range p.seen() {
			if req.Command() == wire.SMB2_CREATE {
				t.Errorf("a CREATE reached the share: %v", req)
			}
		}
	})
}

// TestSMBReadsRefuseInvalidPaths checks that Open and ReadDir refuse every
// invalid path before any request reaches the share: no CREATE goes out for
// any of them.
func TestSMBReadsRefuseInvalidPaths(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{file: `a\b`})
		defer p.stop()
		ctx := t.Context()
		s := p.mustDial(t, ctx)
		defer s.Close()
		for _, path := range invalidRemovePaths {
			if _, err := s.Open(ctx, path); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("Open(%q) = %v, want fs.ErrInvalid", path, err)
			}
			if path == "." {
				continue // the root, which ReadDir lists
			}
			if _, err := s.ReadDir(ctx, path); !errors.Is(err, fs.ErrInvalid) {
				t.Errorf("ReadDir(%q) = %v, want fs.ErrInvalid", path, err)
			}
		}
		for _, req := range p.seen() {
			if req.Command() == wire.SMB2_CREATE {
				t.Errorf("a CREATE reached the share: %v", req)
			}
		}
	})
}

// TestSMBReadDirOfAFile checks that the share's STATUS_NOT_A_DIRECTORY
// becomes ErrNotDir.
func TestSMBReadDirOfAFile(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := startPeer(t, &peer{notDir: `MyWorks\a.fit`})
		defer p.stop()
		ctx := t.Context()
		s := p.mustDial(t, ctx)
		defer s.Close()
		if _, err := s.ReadDir(ctx, "MyWorks/a.fit"); !errors.Is(err, ErrNotDir) {
			t.Errorf("ReadDir of a file = %v, want ErrNotDir", err)
		}
	})
}
