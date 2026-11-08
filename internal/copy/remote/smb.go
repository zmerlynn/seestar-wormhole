package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"sync"

	"github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
	"github.com/hirochachacha/go-smb2/v2/x/protocol"

	"github.com/zmerlynn/seestar-wormhole/internal/copy/guard"
)

// statusNotADirectory is STATUS_NOT_A_DIRECTORY ([MS-ERREF] 2.3.1).
const statusNotADirectory = 0xC0000103

// SMB is a Session on the telescope's share, through go-smb2.
//
// go-smb2 does not give up on a request it has sent when that request's
// context is canceled: it waits for the server's answer, or for the
// connection to close. So an SMB method whose ctx is canceled may not return
// while a server that is connected but silent holds its request. The ctx
// given to Dial is the session's lifetime; canceling it, or calling Close,
// logs off and closes the connection, which ends every pending request.
type SMB struct {
	endpoint string
	session  *smb2.Session
	share    *smb2.Share
	// stopWatch stops the watcher that closes the session when the Dial
	// context is done.
	stopWatch func() bool
}

var _ Session = (*SMB)(nil)

// Dial connects to endpoint (host:port), logs in as guest with an empty
// password and mounts ShareName. There are no retries.
//
// ctx bounds the session, not just the dial: when it is done, the session
// logs off and closes as if by Close, and pending calls fail. Canceling ctx
// during Dial makes Dial return its error.
//
// log gets the record of a panic recovered in the callbacks ctx runs; nil
// discards it.
func Dial(ctx context.Context, endpoint string, log *slog.Logger) (*SMB, error) {
	return dial(ctx, endpoint, newDialer(), log)
}

// newDialer is the fixed go-smb2 configuration Dial uses.
func newDialer() *smb2.Dialer {
	return &smb2.Dialer{
		Credentials: auth.NTLMCredential{User: "guest", Password: ""},
		// The AAPL extension is for macOS servers; the telescope runs Samba.
		DisableAAPLExtension: true,
	}
}

func dial(ctx context.Context, endpoint string, dialer *smb2.Dialer, log *slog.Logger) (*SMB, error) {
	return dialShare(ctx, endpoint, dialer, ShareName, log)
}

// Reach checks that an SMB server lets a guest in at endpoint, host:port:
// it connects, negotiates and sets up the guest session Dial would, then
// drops the connection without logging off. It mounts no share, so it
// takes about half of Test's round trips.
func Reach(ctx context.Context, endpoint string) error {
	return reach(ctx, endpoint, newDialer())
}

func reach(ctx context.Context, endpoint string, dialer *smb2.Dialer) error {
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		return fmt.Errorf("endpoint %q is not host:port", endpoint)
	}
	session, err := dialer.Dial(ctx, endpoint)
	if err != nil {
		return fmt.Errorf("dial %s: %w", endpoint, err)
	}
	_ = session.Abort()
	return nil
}

// closeSession and abortSession are the session calls ctx's callbacks
// make. Tests replace them to panic.
var (
	closeSession = (*smb2.Session).Close
	abortSession = (*smb2.Session).Abort
)

// dialShare is dial for the share named share; only tests name another.
func dialShare(ctx context.Context, endpoint string, dialer *smb2.Dialer, share string, log *slog.Logger) (*SMB, error) {
	// go-smb2 would add port 445 to a bare host, and an empty host dials
	// the local machine; both would connect somewhere the caller did not name.
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("endpoint %q is not host:port", endpoint)
	}
	return open(ctx, endpoint, endpoint, dialer, share, log)
}

// open dials server with dialer, logs in and mounts share. server is the
// name go-smb2 is given; name is the one error text shows.
func open(ctx context.Context, name, server string, dialer *smb2.Dialer, share string, log *slog.Logger) (*SMB, error) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// go-smb2's Dial closes its connection itself when ctx is canceled.
	session, err := dialer.Dial(ctx, server)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", name, err)
	}
	mounted, err := mount(ctx, session, share, log)
	if err != nil {
		return nil, fmt.Errorf("mount %q on %s: %w", share, name, err)
	}
	stop := context.AfterFunc(ctx, func() {
		defer guard.Recover(log, "closing the device session")
		_ = closeSession(session)
	})
	return &SMB{endpoint: name, session: session, share: mounted, stopWatch: stop}, nil
}

// Conn opens a stream to the telescope's SMB server. It returns once the
// stream is open, or with ctx's error if ctx ends first.
type Conn func(context.Context) (net.Conn, error)

// viaServer is the server name DialVia gives go-smb2. go-smb2 needs one; it
// names the server in the NTLM exchange and takes the share path's host from
// it. A fixed loopback name carries nothing about the user's settings.
const viaServer = "127.0.0.1:445"

// DialVia is Dial over a stream conn opens, in place of a TCP connection
// to an endpoint. name is shown in error text only and is never dialed or
// parsed. If the login or mount fails, the stream is closed.
func DialVia(ctx context.Context, name string, conn Conn, log *slog.Logger) (*SMB, error) {
	return dialVia(ctx, name, conn, newDialer(), log)
}

func dialVia(ctx context.Context, name string, conn Conn, dialer *smb2.Dialer, log *slog.Logger) (*SMB, error) {
	via := &connTransport{conn: conn}
	d := *dialer
	d.TransportDialer = via
	s, err := open(ctx, name, viaServer, &d, ShareName, log)
	if err != nil {
		via.close()
		return nil, err
	}
	return s, nil
}

// connTransport is a go-smb2 TransportDialer over the streams conn opens.
// It keeps each stream it handed out, so DialVia can close them on failure
// whatever go-smb2 has already closed.
type connTransport struct {
	conn Conn

	mu    sync.Mutex
	conns []*onceConn
}

func (c *connTransport) Dial(ctx context.Context, _ string) (smb2.Transport, error) {
	nc, err := c.conn(ctx)
	if err != nil {
		return nil, err
	}
	oc := &onceConn{Conn: nc}
	c.mu.Lock()
	c.conns = append(c.conns, oc)
	c.mu.Unlock()
	return smb2.NewTransport(oc), nil
}

// close closes every stream Dial handed out.
func (c *connTransport) close() {
	c.mu.Lock()
	conns := c.conns
	c.conns = nil
	c.mu.Unlock()
	for _, oc := range conns {
		_ = oc.Close()
	}
}

// onceConn closes its stream once, however often it is closed.
type onceConn struct {
	net.Conn
	once sync.Once
	err  error
}

func (c *onceConn) Close() error {
	c.once.Do(func() { c.err = c.Conn.Close() })
	return c.err
}

// mount mounts share. On failure it has closed the session. Canceling
// ctx aborts the session, so a server that never answers TREE_CONNECT cannot
// hold mount; mount returns only once that abort has finished. A panic in
// the abort is logged to log after aborted is closed, so mount never waits
// on the log.
func mount(ctx context.Context, session *smb2.Session, share string, log *slog.Logger) (*smb2.Share, error) {
	aborted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		p := guard.Catch(func() { _ = abortSession(session) })
		close(aborted)
		_ = p.Report(log, "aborting the device session")
	})
	mounted, err := session.Mount(ctx, share)
	if !stop() {
		<-aborted
		return nil, ctx.Err()
	}
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return mounted, nil
}

// Walk lists the share; see Session.
func (s *SMB) Walk(ctx context.Context, skip func(string, bool) bool, kept func(Entry)) ([]Entry, error) {
	entries, err := walk(ctx, s.share.WithContext(ctx), skip, kept)
	if err != nil {
		return nil, fmt.Errorf("walk on %s: %w", s.endpoint, err)
	}
	return entries, nil
}

// ReadDir lists one folder; see Session. go-smb2 opens the folder with
// read-only access and FILE_OPEN, queries its entries and closes it.
func (s *SMB) ReadDir(ctx context.Context, dir string) ([]Entry, error) {
	if !validDir(dir) {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: fs.ErrInvalid}
	}
	entries, err := readDir(s.share.WithContext(ctx), dir)
	if rerr, ok := errors.AsType[*protocol.ResponseError](err); ok && rerr.Code == statusNotADirectory {
		return nil, &fs.PathError{Op: "readdir", Path: dir, Err: ErrNotDir}
	}
	if err != nil {
		return nil, fmt.Errorf("list %q on %s: %w", dir, s.endpoint, err)
	}
	return entries, nil
}

// Open opens path read-only. go-smb2's Share.Open asks for GENERIC_READ with
// FILE_OPEN, so it never creates the file, and shares it for reading and
// writing; it has no way to also share it for deletion.
func (s *SMB) Open(ctx context.Context, path string) (File, error) {
	if !ValidPath(path) {
		return nil, &fs.PathError{Op: "open", Path: path, Err: fs.ErrInvalid}
	}
	f, err := s.share.Open(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("open %q on %s: %w", path, s.endpoint, err)
	}
	return &smbFile{ctx: ctx, f: f}, nil
}

// Close logs off and closes the connection, which ends every pending call.
// go-smb2 bounds the log-off wait for a server that does not answer. Close
// is safe to call more than once and concurrently.
func (s *SMB) Close() error {
	s.stopWatch()
	return s.session.Close()
}

// smbFile binds a go-smb2 File to its Open context and exposes only reads.
type smbFile struct {
	ctx context.Context
	f   *smb2.File
}

func (f *smbFile) Read(p []byte) (int, error) { return f.f.Read(f.ctx, p) }

// Seek moves the file's offset; io.SeekEnd asks the server for the size.
func (f *smbFile) Seek(offset int64, whence int) (int64, error) {
	return f.f.Seek(f.ctx, offset, whence)
}

// Stat asks the server for the open file's information.
func (f *smbFile) Stat() (fs.FileInfo, error) { return f.f.Stat(f.ctx) }

// WriteTo uses go-smb2's pipelined copy.
func (f *smbFile) WriteTo(w io.Writer) (int64, error) { return f.f.WriteTo(f.ctx, w) }

// Close uses the Open context, so it returns at once after a cancel; the
// server then drops the handle when the session logs off.
func (f *smbFile) Close() error { return f.f.Close(f.ctx) }
