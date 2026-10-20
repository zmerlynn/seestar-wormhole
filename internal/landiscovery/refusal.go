package landiscovery

import "time"

// Refusal counts use fixed labels and cumulative uint64 counters.
// They retain no peer addresses, request IDs, methods or device identity.
const (
	refusalMalformed = iota
	refusalOversize
	refusalUnknownMethod
	refusalMissingID
	refusalInvalidMethod
	refusalRate
	refusalOutput
	refusalMetadata
	refusalInterface
	refusalInspection
	refusalSource
	refusalAlias
	refusalDelivery
	refusalCount
)

var refusalRules = [refusalCount]string{
	"malformed_scan", "oversized_scan", "unknown_method", "missing_id",
	"invalid_method", "reply_rate", "oversized_reply", "packet_metadata",
	"interface_unavailable", "inspection_rate", "wrong_source", "ambiguous_alias",
	"reply_delivery_failed",
}

const refusalInterval = 5 * time.Second

func (m *Machine) refuse(rule int) {
	if m.refusals[rule] != ^uint64(0) {
		m.refusals[rule]++
	}
}

// The socket owner flushes on its read deadline even if traffic stops, and on
// exit. Remote input never causes one WARN per datagram.
func (s *Socket) summarize(m *Machine, now time.Duration, final bool) {
	if !final && now-s.summaryLast < refusalInterval {
		return
	}
	s.summaryLast = max(now, s.summaryLast)
	for rule, count := range m.refusals {
		if count != 0 && s.Logger != nil {
			s.Logger.Warn("lan_discovery_refused", "rule", refusalRules[rule], "count", count)
		}
		m.refusals[rule] = 0
	}
}
