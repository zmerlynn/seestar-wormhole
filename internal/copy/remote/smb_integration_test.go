//go:build smbintegration

package remote

// These tests need the Samba fixture in testdata/samba, reachable at
// SEESTAR_COPY_SMB_ENDPOINT (host:port). scripts/copy/test-smb.sh and CI start it.

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/hirochachacha/go-smb2/v2"
	"github.com/hirochachacha/go-smb2/v2/auth"
)

func endpoint(t *testing.T) string {
	t.Helper()
	e := os.Getenv("SEESTAR_COPY_SMB_ENDPOINT")
	if e == "" {
		t.Fatal("SEESTAR_COPY_SMB_ENDPOINT must name the Samba fixture (host:port)")
	}
	return e
}

func dialFixture(t *testing.T) Session {
	t.Helper()
	s, err := Dial(t.Context(), endpoint(t), nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSMBContract(t *testing.T) {
	runContract(t, dialFixture)
}

func TestSMBWriteToPipelines(t *testing.T) {
	s := dialFixture(t)
	defer s.Close()
	e := fixtureFile(t, "MyWorks/big/Stacked_big.fit")
	f, err := s.Open(t.Context(), e.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	// io.Copy would use 32 KiB writes; go-smb2's WriteTo hands over whole
	// server-sized reads.
	w := &cancelingWriter{cancel: func() {}}
	if _, err := io.Copy(w, f); err != nil {
		t.Fatal(err)
	}
	if w.first <= 1<<20 || w.total != e.Size {
		t.Errorf("first write %d bytes, total %d; want more than 1 MiB and %d", w.first, w.total, e.Size)
	}
}

// TestSMBOpenIsReadOnly checks that the File has no way to write, and that
// the fixture share itself refuses writes through go-smb2 directly, so the
// fixture would notice an Open that asked for write access.
func TestSMBOpenIsReadOnly(t *testing.T) {
	s := dialFixture(t)
	defer s.Close()
	const path = "MyWorks/case/a.fit"
	f, err := s.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.(io.Writer); ok {
		t.Error("File is an io.Writer")
	}
	if _, ok := f.(io.WriterAt); ok {
		t.Error("File is an io.WriterAt")
	}
	_ = f.Close()

	dialer := &smb2.Dialer{Credentials: auth.NTLMCredential{User: "guest"}, DisableAAPLExtension: true}
	session, err := dialer.Dial(t.Context(), endpoint(t))
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	share, err := session.Mount(t.Context(), ShareName)
	if err != nil {
		t.Fatal(err)
	}
	for _, flag := range []int{os.O_RDWR, os.O_WRONLY, os.O_WRONLY | os.O_TRUNC, os.O_RDWR | os.O_CREATE} {
		w, err := share.OpenFile(t.Context(), path, flag, 0o644)
		if err == nil {
			_, werr := w.Write(t.Context(), []byte("changed"))
			_ = w.Close(t.Context())
			t.Errorf("OpenFile with flag %#x succeeded (write: %v)", flag, werr)
		} else if !errors.Is(err, os.ErrPermission) {
			t.Errorf("OpenFile with flag %#x = %v, want os.ErrPermission", flag, err)
		}
	}
	if err := share.Mkdir(t.Context(), "MyWorks/new", 0o755); err == nil {
		t.Error("Mkdir succeeded on the fixture share")
	}

	r, err := s.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		t.Fatal(err)
	}
	if want := sha256.Sum256(content(fixtureFile(t, path).Size)); !bytes.Equal(h.Sum(nil), want[:]) {
		t.Error("the file changed")
	}
}
