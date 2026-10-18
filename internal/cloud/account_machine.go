package cloud

import (
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/zmerlynn/seestar-wormhole/internal/core"
)

// AccountEventKind names inputs to the single-owner credential lifecycle.
type AccountEventKind uint8

const (
	AccountBegin AccountEventKind = iota
	AccountCheckProfile
	AccountLoginDone
	AccountProfileDone
	AccountRemoteExpired
	AccountLogout
)

// AccountEvent completions carry the exact action sequence. Remote expiry carries
// the token used by the failed request. Local policy ignores expiry for distinct
// older bytes; identical raw bytes still challenge the current authorization.
type AccountEvent struct {
	Kind     AccountEventKind
	Sequence uint64
	Token    AccessToken
	Err      error
}

// AccountActionKind names effects for the HTTPS edge.
type AccountActionKind uint8

const (
	AccountNone AccountActionKind = iota
	AccountSendLogin
	AccountSendProfile
	AccountComplete
)

// AccountAction transfers secret-bearing values only to the HTTPS adapter.
type AccountAction struct {
	Kind        AccountActionKind
	Sequence    uint64
	Renewal     bool
	Credentials Credentials
	Token       AccessToken
	Err         error
}

// AccountMachine implements 401-driven account replay and
// separate remote expiry path. Deviations are explicit safety policy: no stale
// token replay after failed renewal, no automatic retry of rejected credentials,
// unconditional local cleanup on logout, and strict nonempty token acceptance.
// The edge serializes operations and bounds I/O. No proactive expiry timer exists
// because accessExpire/refreshAfter units and consumers are not established.
type AccountMachine struct {
	credentials       Credentials
	token             AccessToken
	pending           AccountActionKind
	sequence          uint64
	renewals          int
	started, terminal bool
	failure           error
}

func (*AccountMachine) Format(s fmt.State, v rune) { io.WriteString(s, "[account lifecycle]") }
func (*AccountMachine) LogValue() slog.Value       { return slog.StringValue("[account lifecycle]") }

// NewAccountMachine creates a pure owner of ephemeral credentials.
func NewAccountMachine(c Credentials) *AccountMachine { return &AccountMachine{credentials: c} }

// Token borrows the current opaque token, installed before profile retrieval.
func (m *AccountMachine) Token() AccessToken { return m.token }
func (m *AccountMachine) send(kind AccountActionKind) AccountAction {
	m.sequence++
	m.pending = kind
	return AccountAction{Kind: kind, Sequence: m.sequence, Renewal: m.renewals > 0, Credentials: m.credentials, Token: m.token}
}
func (m *AccountMachine) complete(err error) AccountAction {
	m.pending = AccountNone
	return AccountAction{Kind: AccountComplete, Token: m.token, Err: err}
}
func (m *AccountMachine) clear(err error) AccountAction {
	m.credentials = Credentials{}
	m.token = AccessToken{}
	m.terminal = true
	m.failure = JoinOutcomes(m.failure, err)
	return m.complete(m.failure)
}

// Step performs no I/O. now is unused: expiry is response-driven, never inferred
// from wall time or opaque metadata. Completions must be sanitized by the edge.
// Bounded replay and remote expiry are distinct; terminal failures and stale-token protection are local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#account-client-401-driven-credential-replay
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#remote-client-token-expired-event
func (m *AccountMachine) Step(e AccountEvent, now core.Instant) AccountAction {
	if e.Kind == AccountLogout {
		return m.clear(JoinOutcomes(e.Err, ErrReauthentication))
	}
	if e.Kind == AccountRemoteExpired {
		if e.Token == m.token {
			return m.clear(ErrReauthentication)
		}
		return AccountAction{}
	}
	if m.terminal {
		return m.complete(m.failure)
	}
	switch e.Kind {
	case AccountBegin, AccountCheckProfile:
		if m.pending != AccountNone {
			return AccountAction{}
		}
		m.renewals = 0
		if !m.started {
			m.started = true
			return m.send(AccountSendLogin)
		}
		if e.Kind == AccountCheckProfile {
			return m.send(AccountSendProfile)
		}
		return m.complete(nil)
	case AccountLoginDone:
		if m.pending != AccountSendLogin || e.Sequence != m.sequence {
			return AccountAction{}
		}
		if e.Err != nil {
			// Only the canonical retryable result permits fresh credentials. A
			// late completed malformed/denied/fatal reply remains terminal even
			// when it also contains the caller's deadline or cancellation.
			if Disposition(e.Err) == Retryable {
				m.token = AccessToken{}
				m.started = false
				return m.complete(JoinOutcomes(e.Err))
			}
			a := m.clear(e.Err)
			var rejection *AccountError
			if m.renewals > 0 && errors.As(e.Err, &rejection) && (rejection.Status == 401 || rejection.Status == 403) {
				// The original completion owns its received status. Later work
				// needs reauthentication; that status establishes no specific cause.
				// Independent policy causes remain intact; credentials stay cleared.
				m.failure = reauthenticationOutcome(e.Err)
			}
			return a
		}
		if e.Token.value == "" {
			return m.clear(ErrInvalid)
		}
		m.token = e.Token
		return m.send(AccountSendProfile)
	case AccountProfileDone:
		if m.pending != AccountSendProfile || e.Sequence != m.sequence {
			return AccountAction{}
		}
		// A 401 challenge permits replay, but cannot consume an internal failure.
		if Disposition(e.Err) == Fatal {
			return m.clear(e.Err)
		}
		if errors.Is(e.Err, ErrUnauthorized) {
			// Only the ordinary challenge is resolved by credential replay. Any
			// independent blocking observation remains part of this outcome.
			for _, blocking := range []error{ErrInvalid, ErrRejected, ErrBusy, ErrAccountUncertain, ErrOwedRelease, ErrReauthentication} {
				if errors.Is(e.Err, blocking) {
					return m.clear(e.Err)
				}
			}
			if m.renewals >= 2 || m.credentials.email == "" || m.credentials.password == "" {
				return m.clear(JoinOutcomes(e.Err, ErrReauthentication))
			}
			m.renewals++
			return m.send(AccountSendLogin)
		}
		// Retain a barrier for automatic authorization even if the caller has
		// already timed out. Pure I/O failure leaves the installed token usable.
		if Disposition(e.Err) >= Malformed {
			return m.clear(e.Err)
		}
		return m.complete(JoinOutcomes(e.Err))
	}
	return AccountAction{}
}

// reauthenticationOutcome retains policy causes for later work, rather than
// attributing a previous replay's HTTP status or diagnostic wrappers to it.
// JoinOutcomes has already flattened every wrapper's causes into this bounded
// outcome. Keep its leaves and release metadata; the original completion keeps
// the full received diagnostics.
func reauthenticationOutcome(err error) error {
	observed := JoinOutcomes(err).(*Outcome)
	causes := []error{ErrReauthentication}
	for _, cause := range observed.causes {
		if cause == ErrRejected || cause == ErrUnauthorized {
			continue
		}
		if _, wrapped := cause.(interface{ Unwrap() error }); wrapped {
			continue
		}
		if _, joined := cause.(interface{ Unwrap() []error }); joined {
			continue
		}
		causes = append(causes, cause)
	}
	retained := JoinOutcomes(causes...).(*Outcome)
	retained.severity = max(retained.severity, observed.severity)
	retained.releaseBlocked = observed.releaseBlocked
	return retained
}
