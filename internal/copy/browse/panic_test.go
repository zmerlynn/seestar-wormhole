package browse

import (
	"context"
	"testing"
	"testing/fstest"
	"testing/synctest"
	"time"
)

// A panic in a device Read or Seek of a multipart response's producer,
// which runs outside the handler's recovery, fails the response as a device
// error: it is logged, the session retires, and the next request dials again.
func TestMultipartPanic(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*spy)
	}{
		{"read", func(s *spy) { s.readFault = func() error { panic("read panic") } }},
		{"seek", func(s *spy) {
			s.seekFault = func(o int64, _ int) error {
				if o == 20 {
					panic("seek panic")
				}
				return nil
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := true
			e := newEnv(t, envOptions{inApp: true, setup: func(s *spy) {
				if first {
					tt.setup(s)
				}
				first = false
			}})
			e.serve("GET", fitURL, "Range", "bytes=0-9,20-29")
			if !e.d.session(0).closed() || e.logs.count("internal error recovered", tt.name+" panic") != 1 ||
				e.logs.count("session closed", "reason=failed") != 1 {
				t.Errorf("the panic did not fail the session; logs:\n%s", e.logs)
			}
			if w := e.serve("GET", fitURL); w.Code != 200 || e.d.dials() != 2 {
				t.Errorf("next request = %d after %d dials", w.Code, e.d.dials())
			}
		})
	}
}

// A session whose Close panics still retires from the idle timer, the
// cancellation grace, a failed call and the pool's Close: the panic is
// logged, and the session's context still ends.
func TestClosePanic(t *testing.T) {
	retire := map[string]func(p *Sessions){
		"idle": func(p *Sessions) {
			mustGet(t, p, context.Background()).Release()
			time.Sleep(idleTimeout)
		},
		"stuck": func(p *Sessions) {
			ctx, cancel := context.WithCancel(context.Background())
			l := mustGet(t, p, ctx)
			cancel()
			time.Sleep(cancelGrace)
			synctest.Wait()
			l.Release()
		},
		"failed": func(p *Sessions) {
			l := mustGet(t, p, context.Background())
			l.Fail(context.DeadlineExceeded)
			l.Release()
		},
		"stopped": func(p *Sessions) {
			mustGet(t, p, context.Background()).Release()
			p.Close()
		},
	}
	for reason, f := range retire {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := &dialer{fsys: fstest.MapFS{}, setup: func(s *spy) { s.closePanic = true }}
				p, logs := newPool(d)
				defer p.Close()
				f(p)
				synctest.Wait()
				s := d.session(0)
				if !s.closed() || s.ctx.Err() == nil {
					t.Errorf("closed %v, context %v", s.closed(), s.ctx.Err())
				}
				if logs.count("internal error recovered", "close panic") != 1 || logs.count("session closed", "reason="+reason) != 1 {
					t.Errorf("logs:\n%s", logs)
				}
			})
		})
	}
}
