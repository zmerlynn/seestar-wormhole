package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/zmerlynn/seestar-copy/internal/remote"
)

// closeCounter counts the Close calls of the session it wraps.
type closeCounter struct {
	remote.Session
	closes int
}

func (c *closeCounter) Close() error {
	c.closes++
	return c.Session.Close()
}

func useDial(t *testing.T, d func(context.Context, string) (remote.Session, error)) {
	old := dial
	t.Cleanup(func() { dial = old })
	dial = d
}

// Test lists the share root, and closes the session.
func TestTest(t *testing.T) {
	sess := &closeCounter{Session: remote.NewFake(fstest.MapFS{"MyWorks/M 42/a.fit": file("a"), "log.txt": file("l")})}
	var endpoints []string
	useDial(t, func(_ context.Context, endpoint string) (remote.Session, error) {
		endpoints = append(endpoints, endpoint)
		return sess, nil
	})
	entries, err := Test(t.Context(), "device:445")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Path+" "+e.Kind.String())
	}
	if want := []string{"MyWorks dir", "log.txt regular"}; !slices.Equal(got, want) {
		t.Errorf("got entries %q, want %q", got, want)
	}
	if !slices.Equal(endpoints, []string{"device:445"}) || sess.closes != 1 {
		t.Errorf("got dials %q and %d closes, want one dial of device:445 and one close", endpoints, sess.closes)
	}
}

// Test fails with what failed: the connection or the listing.
func TestTestFails(t *testing.T) {
	useDial(t, func(context.Context, string) (remote.Session, error) { return nil, errors.New("no route") })
	if _, err := Test(t.Context(), "device:445"); err == nil || err.Error() != "connect to device:445: no route" {
		t.Errorf("got err %v, want the connect error", err)
	}
	sess := remote.NewFake(fstest.MapFS{})
	sess.Close()
	useDial(t, func(context.Context, string) (remote.Session, error) { return sess, nil })
	if _, err := Test(t.Context(), "device:445"); err == nil || err.Error() != "list the share: readdir .: file already closed" {
		t.Errorf("got err %v, want the listing error", err)
	}
}
