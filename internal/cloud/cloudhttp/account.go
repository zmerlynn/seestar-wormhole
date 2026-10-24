package cloudhttp

import (
	"bytes"
	"context"
	"errors"
	"github.com/go-json-experiment/json/jsontext"
	"net/url"
	"sync"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

func accountOrigin(base string) (*url.URL, error) {
	if base == "" {
		base = "https://api.seestar.com"
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, cloud.ErrConfig
	}
	return u, nil
}

type accountReply struct {
	token cloud.AccessToken
	err   error
}
type accountJob struct {
	ctx   context.Context
	event cloud.AccountEvent
	reply *accountCompletion
}

// accountOwner admits one operation at a time, with no internal request queue or
// per-caller goroutine. Its sole goroutine owns every Step and credential value.
// A caller's timeout includes admission; cancellation never waits for the owner.
type accountOwner struct {
	jobs    chan accountJob
	expired chan struct{}
	done    chan struct{}
	cancel  context.CancelFunc

	// expiryMu protects only the bounded edge mailbox, never protocol state.
	// It is never held across I/O, Step, callbacks or channel operations.
	expiryMu sync.Mutex
	// correlationToken is never authorization. Retain just the last published
	// token through tokenless cancellation until replacement or shutdown.
	currentToken, correlationToken     string
	pendingExpiry, expiryTaken, closed bool
}

func newAccountOwner(c *Client, base *url.URL, credentials cloud.Credentials) *accountOwner {
	life, cancel := context.WithCancel(context.Background())
	o := &accountOwner{jobs: make(chan accountJob), expired: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel}
	edge := &Client{account: o, logger: c.logger, base: base, transport: c.transport, timeout: c.timeout}
	go func() {
		defer close(o.done)
		m := cloud.NewAccountMachine(credentials)
		credentials = cloud.Credentials{}
		defer edge.accountStep(m, cloud.AccountEvent{Kind: cloud.AccountLogout})
		expire := func() {
			token := o.takeExpiry()
			if token != "" && (m.Token().Value() == "" || token == m.Token().Value()) {
				// The mailbox already correlated the failed request. A tokenless
				// machine has retired authorization, not the remote expiry obligation.
				edge.accountStep(m, cloud.AccountEvent{Kind: cloud.AccountRemoteExpired, Token: m.Token()})
				o.publishToken(m.Token().Value())
			}
		}
		for {
			// The wake channel coalesces notifications; the mailbox retains expiry
			// independently of caller cancellation. Drain it before admitting work.
			select {
			case <-o.expired:
				expire()
			default:
			}
			select {
			case <-life.Done():
				return
			case <-o.expired:
				expire()
			case j := <-o.jobs:
				if life.Err() != nil {
					// Admission can win the shutdown select. Even without issuing a
					// request, the owner owes every admitted waiter a publication.
					a := edge.accountStep(m, cloud.AccountEvent{Kind: cloud.AccountLogout})
					j.reply.publish(accountReply{err: a.Err})
					return
				}
				select {
				case <-o.expired:
					expire()
				default:
				}
				ctx, cancel := context.WithTimeout(j.ctx, edge.timeout)
				stop := context.AfterFunc(life, cancel)
				result := runAccount(ctx, edge, m, j.event)
				if o.publishToken(m.Token().Value()) {
					// Expiry remains correlated through tokenless retirement. A distinct
					// installed replacement has already reset the old correlation.
					var rejection *cloud.AccountError
					if !errors.As(result.err, &rejection) || rejection.Status == 0 || !errors.Is(rejection, cloud.ErrRejected) {
						a := edge.accountStep(m, cloud.AccountEvent{Kind: cloud.AccountRemoteExpired, Token: m.Token()})
						result = accountReply{token: a.Token, err: cloud.JoinOutcomes(result.err, a.Err)}
					}
				}
				stop()
				cancel()
				j.reply.publish(result) // completion remains owned after caller cancellation
			}
		}
	}()
	return o
}
func (o *accountOwner) close() {
	o.cancel()
	<-o.done
	// Release the mailbox's token even if shutdown wins a queued wakeup.
	o.expiryMu.Lock()
	o.currentToken, o.correlationToken, o.pendingExpiry, o.expiryTaken, o.closed = "", "", false, false, true
	o.expiryMu.Unlock()
	select {
	case <-o.expired:
	default:
	}
}

// publishToken replaces correlation only with nonempty authorization. Tokenless
// retirement retains one correlation value for a late remote 401. A queued expiry
// is delivered once here, atomically with expire, even if cancellation has already
// cleared the machine token. Reusing the same token preserves pending expiry.
func (o *accountOwner) publishToken(token string) bool {
	o.expiryMu.Lock()
	if token != "" && token != o.correlationToken {
		o.correlationToken, o.pendingExpiry, o.expiryTaken = token, false, false
	}
	expired := token == "" && o.pendingExpiry
	if expired {
		o.pendingExpiry, o.expiryTaken = false, true
	}
	o.expiryMu.Unlock()
	return expired
}
func (o *accountOwner) takeExpiry() string {
	o.expiryMu.Lock()
	defer o.expiryMu.Unlock()
	if !o.pendingExpiry {
		return ""
	}
	o.pendingExpiry, o.expiryTaken = false, true
	return o.correlationToken
}
func (o *accountOwner) expire(token string) {
	o.expiryMu.Lock()
	if o.closed || o.expiryTaken || token == "" || token != o.correlationToken {
		o.expiryMu.Unlock()
		return
	}
	o.pendingExpiry = true
	o.expiryMu.Unlock()
	// Duplicates share one persistent flag. A full wake channel already promises
	// service; no sender waits for the owner or an unrelated account operation.
	select {
	case o.expired <- struct{}{}:
	default:
	}
}
func (o *accountOwner) call(ctx context.Context, e cloud.AccountEvent) accountReply {
	j := accountJob{ctx: ctx, event: e, reply: newAccountCompletion()}
	select {
	case <-o.done:
		return accountReply{err: cloud.ErrReauthentication}
	default:
	}
	select {
	case <-ctx.Done():
		return accountReply{err: ctx.Err()}
	case <-o.done:
		return accountReply{err: cloud.ErrReauthentication}
	case o.jobs <- j:
	}
	select {
	case <-ctx.Done():
		return canceledAccountReply(j.reply, ctx.Err())
	case <-o.done:
		return canceledAccountReply(j.reply, cloud.ErrReauthentication)
	case <-j.reply.done:
		r := j.reply.result
		r.err = cloud.JoinOutcomes(r.err, ctx.Err())
		return r
	}
}

func canceledAccountReply(reply *accountCompletion, interrupted error) accountReply {
	// Prefer a published classification when cancellation and completion race.
	// Otherwise return promptly with a handle to the owner's pending completion.
	select {
	case <-reply.done:
		r := reply.result
		r.err = cloud.JoinOutcomes(r.err, interrupted)
		return r
	default:
		return accountReply{err: cloud.JoinOutcomes(interrupted, &pendingAccount{reply})}
	}
}
func (c *Client) accountStep(m *cloud.AccountMachine, e cloud.AccountEvent) cloud.AccountAction {

	a := m.Step(e, 0)
	if c.account != nil {
		// Publish authorization at installation/retirement, even while profile
		// I/O is pending. Cleanup only borrows it; it never admits account work.
		c.account.expiryMu.Lock()
		token := m.Token().Value()
		c.account.currentToken = token
		if token != "" && token != c.account.correlationToken {
			// A stop can already use this token while profile I/O is pending,
			// so its 401 must correlate now too. Identical bytes retain expiry.
			c.account.correlationToken, c.account.pendingExpiry, c.account.expiryTaken = token, false, false
		}
		c.account.expiryMu.Unlock()
	}
	return a
}

func runAccount(ctx context.Context, c *Client, m *cloud.AccountMachine, e cloud.AccountEvent) (result accountReply) {
	// An injected transport is not allowed to expose secret-bearing panic text.
	defer func() {
		if recover() != nil {
			a := c.accountStep(m, cloud.AccountEvent{Kind: cloud.AccountLogout, Err: cloud.JoinOutcomes(result.err, cloud.ErrInternal)})
			result = accountReply{err: a.Err}
		}
	}()
	if ctx.Err() != nil {
		return accountReply{err: ctx.Err()}
	}
	a := c.accountStep(m, e)
	for a.Kind != cloud.AccountComplete {
		if a.Kind == cloud.AccountNone {
			return accountReply{err: cloud.ErrInvalid}
		}
		request := *c
		request.token = a.Token.Value()
		var next cloud.AccountEvent
		if a.Kind == cloud.AccountSendLogin {
			phase := "login"
			if a.Renewal {
				phase = "renewal"
			}
			finish := diagnostic.Operation(c.logger, phase)
			request.token = ""
			requestCtx := ctx
			cancel := func() {}
			if a.Renewal {
				requestCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
			}
			info, b, err := request.exchangeRaw(requestCtx, "/v1/user/login", nil, a.Credentials.LoginBody(a.Renewal), nil, true, a.Renewal)
			token, parseErr := cloud.ParseLogin(info.status, b, a.Renewal)
			c.rejected(requestCtx, phase, info, b, accountResponseError(info, b, err, parseErr))
			next = cloud.AccountEvent{Kind: cloud.AccountLoginDone, Sequence: a.Sequence, Token: token, Err: accountResponseError(info, b, err, parseErr)}
			finish(next.Err)
			cancel()
		} else {
			finish := diagnostic.Operation(c.logger, "account_profile")
			info, b, err := request.exchangeRaw(ctx, "/v1/user/current-profile", nil, nil, nil, true, false)
			parseErr := cloud.ParseProfile(info.status, b)
			c.rejected(ctx, "account_profile", info, b, accountResponseError(info, b, err, parseErr))
			next = cloud.AccountEvent{Kind: cloud.AccountProfileDone, Sequence: a.Sequence, Err: accountResponseError(info, b, err, parseErr)}
			finish(next.Err)
		}
		next.Err = cloud.JoinOutcomes(next.Err, ctx.Err())
		a = c.accountStep(m, next)
	}
	return accountReply{token: a.Token, err: a.Err}
}
func accountResponseError(info responseInfo, body []byte, ioErr, parseErr error) error {
	if info.temporary {
		return cloud.JoinOutcomes(cloud.ErrTransport, ioErr)
	}
	status := info.status
	// Account parsers own login/profile status semantics, including bounded
	// profile renewal. Join only established parser observations, as for remote I/O.
	result := cloud.JoinOutcomes(ioErr)
	if status == 0 && ioErr == nil {
		return cloud.JoinOutcomes(result, cloud.ErrInvalid)
	}
	if status != 0 && (info.complete || ioErr == nil || (len(body) <= cloud.MaxBody && jsontext.Value(body).IsValid()) ||
		errors.Is(parseErr, cloud.ErrRejected) || errors.Is(parseErr, cloud.ErrUnauthorized)) {
		result = cloud.JoinOutcomes(result, parseErr)
	}
	return result
}

// Login installs a token then fetches the profile. It is lazy/idempotent after
// installation. Blocking failures survive cancellation. After a rejected replay,
// later calls require reauthentication without attributing its HTTP status to a
// specific cause. Pure profile I/O preserves the token.
func (c *Client) Login(ctx context.Context) error {
	if c.account == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.account.call(ctx, cloud.AccountEvent{Kind: cloud.AccountBegin}).err
}

// CheckProfile performs an account request, renewing on 401 at most twice. Remote
// calls never invoke this authenticator. Concurrent account operations serialize
// and use the latest token; no duplicate in-flight password replay is possible.
func (c *Client) CheckProfile(ctx context.Context) error {
	if c.account == nil {
		return cloud.ErrConfig
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	return c.account.call(ctx, cloud.AccountEvent{Kind: cloud.AccountCheckProfile}).err
}
func (c *Client) authorized(ctx context.Context) (*Client, error) {
	if c.account == nil {
		return c, nil
	}
	r := c.account.call(ctx, cloud.AccountEvent{Kind: cloud.AccountBegin})
	if r.err != nil {
		return nil, r.err
	}
	// Ordinary remote operations borrow an immutable per-request snapshot.
	return &Client{logger: c.logger, base: c.base, transport: c.transport, token: r.token.Value(), source: c.source, timeout: c.timeout, stopTimeout: c.stopTimeout}, nil
}

// Remote 401 is not account replay; matching token bytes fence stale responses locally:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login/lifecycle.md#remote-client-token-expired-event
func (c *Client) remoteStatus(status int, token string) error {
	if c.account == nil || status != 401 {
		return nil
	}
	// A bounded, prioritized notification survives cancellation of the
	// challenged request. It is consumed before subsequent account work.
	c.account.expire(token)
	return cloud.ErrReauthentication
}
func (c *Client) exchange(ctx context.Context, path string, q url.Values, body []byte, capture *bytes.Buffer) (responseInfo, []byte, error) {
	request, err := c.authorized(ctx)
	if err != nil {
		return responseInfo{}, nil, err
	}
	info, b, err := request.exchangeRaw(ctx, path, q, body, capture, false, false)
	return info, b, cloud.JoinOutcomes(err, c.remoteStatus(info.status, request.token))
}
