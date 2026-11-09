package landiscovery

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParseMode(t *testing.T) {
	for _, c := range []struct {
		in   string
		want Mode
		ok   bool
	}{
		{"start", ScanStart, true}, {"connect", ScanConnect, true}, {"off", ScanOff, true},
		{"", ScanStart, false}, {"Off", ScanStart, false}, {"never", ScanStart, false},
	} {
		got, ok := ParseMode(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("ParseMode(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
		if ModeOf(c.in) != c.want {
			t.Errorf("ModeOf(%q) = %q, want %q", c.in, ModeOf(c.in), c.want)
		}
	}
}

func TestRunFrom(t *testing.T) {
	if RunFrom(context.Background()) != nil {
		t.Fatal("a bare context carries a run")
	}
	r := &Run{}
	if RunFrom(WithRun(context.Background(), r)) != r {
		t.Fatal("the run isn't carried")
	}
}

// runCounter is a target for InRun: search finds it at found[i] for its
// i-th search, and dial connects at any address in up.
type runCounter struct {
	mu       sync.Mutex
	found    []string
	up       map[string]bool
	searches int
	dials    []string
}

func (c *runCounter) dial(_ context.Context, at string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dials = append(c.dials, at)
	if !c.up[at] {
		return "", errors.New("refused")
	}
	return at, nil
}

func (c *runCounter) search(ctx context.Context) (string, string, error) {
	c.mu.Lock()
	i := c.searches
	c.searches++
	c.mu.Unlock()
	if i >= len(c.found) || c.found[i] == "" {
		return "", "", errors.New("not found")
	}
	v, err := c.dial(ctx, c.found[i])
	return v, c.found[i], err
}

func (c *runCounter) connect(t *testing.T, r *Run, key string) (string, bool, error) {
	t.Helper()
	v, at, searched, err := InRun(context.Background(), r, key, c.dial, c.search)
	if err == nil && v != at {
		t.Fatalf("session for %q reported at %q", v, at)
	}
	return at, searched, err
}

func TestInRunScansOnce(t *testing.T) {
	c := &runCounter{found: []string{"a"}, up: map[string]bool{"a": true}}
	r := &Run{}
	for i := range 5 {
		at, searched, err := c.connect(t, r, "k")
		if err != nil || at != "a" || searched != (i == 0) {
			t.Fatalf("connect %d: %q searched=%v %v", i, at, searched, err)
		}
	}
	if c.searches != 1 {
		t.Fatalf("searches = %d, want 1", c.searches)
	}
}

func TestInRunRescansOnceAfterAFailedConnect(t *testing.T) {
	c := &runCounter{found: []string{"a", "b"}, up: map[string]bool{"a": true, "b": true}}
	r := &Run{}
	if _, _, err := c.connect(t, r, "k"); err != nil {
		t.Fatal(err)
	}
	c.up["a"] = false // the router moved it
	at, searched, err := c.connect(t, r, "k")
	if err != nil || at != "b" || !searched {
		t.Fatalf("after a failed connect: %q searched=%v %v", at, searched, err)
	}
	at, searched, err = c.connect(t, r, "k")
	if err != nil || at != "b" || searched {
		t.Fatalf("after the rescan: %q searched=%v %v", at, searched, err)
	}
	if c.searches != 2 {
		t.Fatalf("searches = %d, want 2", c.searches)
	}
}

func TestInRunFailedSearchKeepsNoAddress(t *testing.T) {
	c := &runCounter{found: []string{"", "a"}, up: map[string]bool{"a": true}}
	r := &Run{}
	if _, _, err := c.connect(t, r, "k"); err == nil {
		t.Fatal("a failed search connected")
	}
	at, searched, err := c.connect(t, r, "k")
	if err != nil || at != "a" || !searched {
		t.Fatalf("after a failed search: %q searched=%v %v", at, searched, err)
	}
	if len(c.dials) != 1 {
		t.Fatalf("dials = %q: a failed search's address was dialed", c.dials)
	}
}

func TestInRunKeysTheTarget(t *testing.T) {
	c := &runCounter{found: []string{"a", "b"}, up: map[string]bool{"a": true, "b": true}}
	r := &Run{}
	c.connect(t, r, "k")
	at, searched, err := c.connect(t, r, "other")
	if err != nil || at != "b" || !searched {
		t.Fatalf("another target: %q searched=%v %v", at, searched, err)
	}
}

func TestInRunSharesASearch(t *testing.T) {
	r := &Run{}
	release := make(chan struct{})
	entered := make(chan struct{})
	var searches atomic.Int32
	search := func(ctx context.Context) (string, string, error) {
		searches.Add(1)
		close(entered)
		<-release
		return "a", "a", nil
	}
	dial := func(_ context.Context, at string) (string, error) { return at, nil }
	var wg sync.WaitGroup
	results := make([]string, 4)
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], _, _, _ = InRun(context.Background(), r, "k", dial, search)
	}()
	<-entered
	for i := 1; i < len(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _, _, _ = InRun(context.Background(), r, "k", dial, search)
		}()
	}
	close(release)
	wg.Wait()
	if searches.Load() != 1 {
		t.Fatalf("searches = %d, want 1", searches.Load())
	}
	for i, v := range results {
		if v != "a" {
			t.Fatalf("connect %d reached %q", i, v)
		}
	}
}

func TestInRunWaitHonorsCancel(t *testing.T) {
	r := &Run{}
	release := make(chan struct{})
	entered := make(chan struct{})
	go InRun(context.Background(), r, "k", func(context.Context, string) (string, error) { return "", nil },
		func(context.Context) (string, string, error) { close(entered); <-release; return "a", "a", nil })
	<-entered
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, _, err := InRun(ctx, r, "k", func(context.Context, string) (string, error) {
		t.Error("dialed while canceled")
		return "", nil
	}, func(context.Context) (string, string, error) {
		t.Error("searched while canceled")
		return "", "", nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want canceled", err)
	}
}
