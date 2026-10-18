package proxy

import (
	"slices"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
	"github.com/zmerlynn/seestar-wormhole/internal/p2ptunnel"
	"github.com/zmerlynn/seestar-wormhole/internal/session"
)

// Local admission policy, independent of the transport liveness and each
// verification attempt's deadline. Wait also accommodates channel quarantine.
// These slots contain no client payload and belong to the existing session owner.
// Fresh-ID re-verification on one session is local behavior, unlike native mapping teardown:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/transport/p2p-tunnel/framing.md#agent-behaviour
const (
	pendingOpenLimit = 8
	pendingOpenWait  = 35 * time.Second
	pendingOpenRetry = time.Second
)

func (x *stack) expireOpenAdmissions(now core.Instant) {
	for id, until := range x.openAdmissions {
		if now < until {
			continue
		}
		delete(x.openAdmissions, id)
		x.pending = slices.DeleteFunc(x.pending, func(v uint64) bool { return v == id })
		x.enqueue(layerEvent{t: &p2ptunnel.Event{Kind: p2ptunnel.CancelOpen, Request: id}})
		if c := x.streams[id]; c != nil {
			c.finish(session.ErrTimeout)
			delete(x.streams, id)
			x.f.s.forget(id)
		}
	}
}

func (x *stack) openAdmissionTimer(now core.Instant) {
	at := x.deadlines[3]
	if len(x.pending) == 0 {
		at = 0
	} else if next := now.Add(pendingOpenRetry); at == 0 || next < at {
		at = next
	}
	for _, until := range x.openAdmissions {
		if at == 0 || until < at {
			at = until
		}
	}
	x.deadlines[3] = at
}
