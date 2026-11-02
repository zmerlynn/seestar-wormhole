package remote

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"

	"github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
)

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
func Dial(ctx context.Context, endpoint string) (*SMB, error) {
	return dial(ctx, endpoint, newDialer())
}

// newDialer is the fixed go-smb2 configuration Dial uses.
func newDialer() *smb2.Dialer {
	return &smb2.Dialer{
		Credentials: auth.NTLMCredential{User: "guest", Password: ""},
		// The AAPL extension is for macOS servers; the telescope runs Samba.
		DisableAAPLExtension: true,
	}
}

func dial(ctx context.Context, endpoint string, dialer *smb2.Dialer) (*SMB, error) {
	// go-smb2 would add port 445 to a bare host, and an empty host dials
	// the local machine; both would connect somewhere the caller did not name.
	if host, port, err := net.SplitHostPort(endpoint); err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("endpoint %q is not host:port", endpoint)
	}
	// go-smb2's Dial closes its connection itself when ctx is canceled.
	session, err := dialer.Dial(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", endpoint, err)
	}
	share, err := mount(ctx, session)
	if err != nil {
		return nil, fmt.Errorf("mount %q on %s: %w", ShareName, endpoint, err)
	}
	stop := context.AfterFunc(ctx, func() { _ = session.Close() })
	return &SMB{endpoint: endpoint, session: session, share: share, stopWatch: stop}, nil
}

// mount mounts ShareName. On failure it has closed the session. Canceling
// ctx aborts the session, so a server that never answers TREE_CONNECT cannot
// hold mount; mount returns only once that abort has finished.
func mount(ctx context.Context, session *smb2.Session) (*smb2.Share, error) {
	aborted := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(aborted)
		_ = session.Abort()
	})
	share, err := session.Mount(ctx, ShareName)
	if !stop() {
		<-aborted
		return nil, ctx.Err()
	}
	if err != nil {
		_ = session.Close()
		return nil, err
	}
	return share, nil
}

// Walk lists the share; see Session.
func (s *SMB) Walk(ctx context.Context, skip func(string, bool) bool) ([]Entry, error) {
	entries, err := walk(ctx, s.share.WithContext(ctx), skip)
	if err != nil {
		return nil, fmt.Errorf("walk on %s: %w", s.endpoint, err)
	}
	return entries, nil
}

// Open opens path read-only. go-smb2's Share.Open asks for GENERIC_READ with
// FILE_OPEN, so it never creates the file, and shares it for reading and
// writing; it has no way to also share it for deletion.
func (s *SMB) Open(ctx context.Context, path string) (File, error) {
	if !validPath(path) {
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

// WriteTo uses go-smb2's pipelined copy.
func (f *smbFile) WriteTo(w io.Writer) (int64, error) { return f.f.WriteTo(f.ctx, w) }

// Close uses the Open context, so it returns at once after a cancel; the
// server then drops the handle when the session logs off.
func (f *smbFile) Close() error { return f.f.Close(f.ctx) }
