// Package cloudhttp is the blocking HTTPS edge for cloud. It reads credentials
// only from the environment, never logs response bodies or transport errors, and
// owns a successful allocation until a lease is explicitly transferred. It starts
// one account owner goroutine when email credentials are supplied. Callers bound
// remote concurrency (one active attempt per device).
//
// Wire reference: https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/account-login.md
package cloudhttp

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"github.com/zmerlynn/seestar-wormhole/internal/oserror"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zmerlynn/seestar-wormhole/internal/cloud"
	"github.com/zmerlynn/seestar-wormhole/internal/diagnostic"
)

// Transport is a single HTTP exchange. Implementations must honor request
// cancellation and Request.Close, must not retry or redirect, and must not log
// request/response material. http.Transport satisfies this for these requests.
type Transport interface {
	RoundTrip(*http.Request) (*http.Response, error)
}

// Config configures edge I/O only. Empty BaseURL selects the vendor production
// HTTPS origin; AccountBaseURL independently overrides the account origin.
// StateDir opts into installation identifier storage; empty keeps it in memory.
// Timeouts are local policy;
// zero selects 30 s / 5 s respectively.
type Config struct {
	Logger                   *slog.Logger // Optional secret-safe phase diagnostics.
	BaseURL                  string
	AccountBaseURL, StateDir string
	Transport                Transport
	Timeout, StopTimeout     time.Duration
}

// ErrCleanup preserves a failed bounded stop during allocation error recovery.
// Adapters must record unconfirmed release before any fresh acquisition, and
// preserve the original acquisition policy. A fresh online-link may recover
// without establishing expiry/reuse of the old allocation.
var ErrCleanup = cloud.ErrOwedRelease

// AcquireError keeps an acquisition failure distinct from its failed bounded
// cleanup. Joined disposition and release obligations MUST use cloud.JoinOutcomes
// over the whole error; the permanent acquisition barrier uses Primary separately.
// Both causes and ErrCleanup remain available through errors.Is/errors.As.
type AcquireError struct {
	Primary error
	Cleanup error
}

// Error returns a fixed message without formatting either cause.
func (*AcquireError) Error() string { return "cloud: allocation and cleanup failed" }

// Unwrap preserves the original failure and the unconfirmed release obligation.
func (e *AcquireError) Unwrap() []error {
	return []error{e.Primary, cloud.ReleaseOutcome(e.Cleanup)}
}

// Device selects the detail and online-link query. It contains no credentials.
type Device struct{ Serial, Model string }

func (Device) LogValue() slog.Value          { return slog.StringValue("[cloud device selection]") }
func (Device) Format(s fmt.State, verb rune) { io.WriteString(s, "[cloud device selection]") }

// Client contains ephemeral environment credentials; do not copy after use.
type Client struct {
	logger               *slog.Logger
	oddityRecords        *atomic.Uint32 // shared bounded diagnostics, safe across client snapshots
	base                 *url.URL
	account              *accountOwner
	transport            Transport
	token, source        string
	timeout, stopTimeout time.Duration
}

func (*Client) LogValue() slog.Value          { return slog.StringValue("[cloud HTTPS client]") }
func (*Client) Format(s fmt.State, verb rune) { io.WriteString(s, "[cloud HTTPS client]") }

// NewFromEnvironment reads only SEESTAR_EMAIL/SEESTAR_PASSWORD for account login.
// Empty StateDir keeps a random installation UUID in memory for this client lifetime.
func NewFromEnvironment(cfg Config) (*Client, error) {
	email, password := os.Getenv("SEESTAR_EMAIL"), os.Getenv("SEESTAR_PASSWORD")
	if email == "" {
		return nil, environmentInputError("SEESTAR_EMAIL", emailRule)
	}
	if password == "" {
		return nil, environmentInputError("SEESTAR_PASSWORD", passwordRule)
	}
	credentials, err := cloud.EmailCredentials(email, password)
	if err != nil {
		var validation *cloud.ValidationError
		if errors.As(err, &validation) && validation.Field == "email" {
			return nil, environmentInputError("SEESTAR_EMAIL", emailRule)
		}
		return nil, environmentInputError("SEESTAR_PASSWORD", passwordRule)
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api-ithings.zwoastro.com"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, cloud.ErrConfig
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = 5 * time.Second
	}
	if cfg.Timeout < 0 || cfg.Timeout > 30*time.Second || cfg.StopTimeout < 0 || cfg.StopTimeout > 30*time.Second {
		return nil, cloud.ErrConfig
	}
	if cfg.Transport == nil {
		cfg.Transport = &http.Transport{DisableKeepAlives: true, MaxConnsPerHost: 2, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: cfg.Timeout, MaxResponseHeaderBytes: 16 << 10, DisableCompression: true}
	}
	c := &Client{logger: cfg.Logger, oddityRecords: new(atomic.Uint32), base: u, transport: cfg.Transport, timeout: cfg.Timeout, stopTimeout: cfg.StopTimeout}
	accountURL, err := accountOrigin(cfg.AccountBaseURL)
	if err != nil {
		return nil, err
	}
	c.source, err = persistentSource(cfg.StateDir)
	if err != nil {
		return nil, err
	}
	if cfg.StateDir == "" && cfg.Logger != nil {
		cfg.Logger.Info("installation_id_ephemeral", "storage", "memory only")
	}
	c.account = newAccountOwner(c, accountURL, credentials)
	return c, nil
}
func validInput(s string) bool {
	return len(s) > 0 && len(s) <= 4096 && !strings.ContainsAny(s, "\x00\r\n") && strings.TrimSpace(s) != ""
}

// Close cancels and joins account operations, clears account credentials, and
// releases idle connections. First close device leases and join remote operations.
func (c *Client) Close() {
	if c.account != nil {
		c.account.close()
	}
	if t, ok := c.transport.(interface{ CloseIdleConnections() }); ok {
		t.CloseIdleConnections()
	}
}
func (c *Client) exchangeRaw(ctx context.Context, path string, q url.Values, body []byte, capture *bytes.Buffer, account, renewal bool) (info responseInfo, response []byte, resultErr error) {
	defer func() {
		if recover() != nil {
			resultErr = cloud.JoinOutcomes(resultErr, cloud.ErrInternal)
		}
		resultErr = cloud.JoinOutcomes(resultErr, ctx.Err())
	}()
	u := *c.base
	u.Path = path
	u.RawQuery = q.Encode()
	method := "GET"
	if body != nil {
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return responseInfo{}, nil, cloud.ErrConfig
	}
	// Suppress net/http's transparent retry of replayable requests, including GET.
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.GetBody = nil
	// The HTTP/1 idle read loop can log unsolicited response bytes verbatim.
	// Close even injected transports' connections after each cloud exchange.
	req.Close = true
	if !renewal {
		if c.token != "" {
			req.Header.Set("Authorization", c.token)
		}
		if account && c.token != "" {
			req.Header.Set("X-Astroimg-Authorization", c.token)
		}
		req.Header.Set("X-Lang", "en")
		req.Header.Set("x-service-platform", "seestar")
		req.Header.Set("country_code", "US")
		req.Header.Set("x-params", `{"channel":"google","platform":"android","model":"","country_code":"US","os_version":"","app_version":"3.3.2"}`)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		if account {
			req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		}
	}
	if account {
		req.Header.Set("User-Agent", "okhttp/4.12.0")
	}
	resp, err := c.transport.RoundTrip(req)
	if resp == nil {
		return responseInfo{}, nil, safeIO(ctx, err)
	}
	info = responseInfo{status: resp.StatusCode, contentType: resp.Header.Get("Content-Type")}
	info.temporary = temporaryResponse(info.status, path, account)
	// Observe headers before touching the body; read/close/panic cannot erase them.
	if info.temporary {
		resultErr = cloud.ErrTransport
	} else if account && path == "/v1/user/login" {
		if resp.StatusCode != 200 && !(renewal && resp.StatusCode >= 200 && resp.StatusCode < 300) {
			_, resultErr = cloud.ParseLogin(resp.StatusCode, nil, renewal)
		}
	} else if account || resp.StatusCode != http.StatusBadRequest {
		// Remote HTTP 400 is classified with its body in responseError: the
		// documented vendor busy code can explain it. All other header outcomes
		// remain independent observations even if body processing fails.
		resultErr = headerOutcome(resp.StatusCode)
	}
	if err != nil {
		resultErr = cloud.JoinOutcomes(resultErr, safeIO(ctx, err))
	}
	if resp.Body == nil {
		return info, nil, cloud.JoinOutcomes(resultErr, cloud.ErrTransport)
	}
	defer func() {
		defer func() {
			if recover() != nil {
				resultErr = cloud.JoinOutcomes(resultErr, cloud.ErrInternal)
			}
		}()
		if closeErr := resp.Body.Close(); closeErr != nil {
			resultErr = cloud.JoinOutcomes(resultErr, safeIO(ctx, closeErr))
		}
	}()
	if info.temporary {
		// The status explains this failure. Do not read an error page as JSON;
		// retry belongs to the existing owner backoff, never the HTTP transport.
		return info, nil, resultErr
	}
	if capture == nil {
		capture = new(bytes.Buffer)
	}
	_, readErr := io.Copy(capture, io.LimitReader(resp.Body, cloud.MaxBody+1))
	info.complete = readErr == nil
	b := capture.Bytes()
	if readErr != nil {
		resultErr = cloud.JoinOutcomes(resultErr, safeIO(ctx, readErr))
	}
	if len(b) > cloud.MaxBody {
		resultErr = cloud.JoinOutcomes(resultErr, cloud.ErrInvalid)
	}
	return info, b, resultErr
}
func safeIO(ctx context.Context, err error) error {
	// Only fixed causes survive, never raw transport messages/URLs. Observe all
	// known causes rather than choosing the first secondary network error.
	result := cloud.JoinOutcomes(cloud.ErrTransport, ctx.Err())
	if oserror.Is(err, syscall.ECONNREFUSED) {
		result = cloud.JoinOutcomes(result, syscall.ECONNREFUSED)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		result = cloud.JoinOutcomes(result, context.DeadlineExceeded)
	}
	var dns *net.DNSError
	if errors.As(err, &dns) && dns.IsNotFound {
		result = cloud.JoinOutcomes(result, &net.DNSError{IsNotFound: true})
	}
	return result
}

// Local retry policy is restricted to throttling, gateway failures and service
// unavailability on requests that cannot create or release a device allocation.
func temporaryResponse(status int, path string, account bool) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
	default:
		return false
	}
	if account {
		return path == "/v1/user/login" || path == "/v1/user/current-profile"
	}
	return path == "/v1/my-device" || path == "/v1/my-device/detail"
}

// Remote 401 requires reauthentication; treating 403 as blocking is local policy:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#response-envelope
func headerOutcome(status int) error {
	// HTTP 401 reports token expiry. 403 and other non-2xx
	// rejections use local blocking policy, independent of body completion.
	if status == 401 || status == 403 {
		return cloud.ErrUnauthorized
	}
	if status != 0 && (status < 200 || status >= 300) {
		return cloud.ErrRejected
	}
	return nil
}
func responseError(info responseInfo, body []byte, parseErr, errorIO error) error {
	if info.temporary {
		return cloud.JoinOutcomes(cloud.ErrTransport, errorIO)
	}
	status := info.status
	headerErr := headerOutcome(status)
	// A complete documented busy reply explains the vendor's HTTP 400. Keep
	// other HTTP denials (especially auth), incomplete replies and independent
	// I/O/cleanup outcomes blocking according to their ordinary disposition.
	if status == http.StatusBadRequest && info.complete && errors.Is(parseErr, cloud.ErrBusy) && errors.Is(cloud.ParseStop(body), cloud.ErrBusy) {
		headerErr = nil
	}
	result := cloud.JoinOutcomes(headerErr, errorIO)
	if status == 0 && errorIO == nil {
		return cloud.JoinOutcomes(result, cloud.ErrRejected)
	}
	// An incomplete body did not establish a malformed primary outcome. Decode
	// with JSON v2; complete semantic-invalid JSON and known vendor replies
	// ARE observed outcomes even if a subsequent read or Close also fails.
	if status != 0 && (info.complete || errorIO == nil || (len(body) <= cloud.MaxBody && jsontext.Value(body).IsValid()) ||
		errors.Is(parseErr, cloud.ErrBusy) || errors.Is(parseErr, cloud.ErrOffline) || errors.Is(parseErr, cloud.ErrRejected)) {
		result = cloud.JoinOutcomes(result, parseErr)
	}
	return result
}

func query(d Device) url.Values { return url.Values{"deviceSn": {d.Serial}, "deviceModel": {d.Model}} }

// Detail obtains and verifies the requested device, including busy/offline state.
func (c *Client) Detail(ctx context.Context, d Device) (cloud.Detail, error) {
	return c.detail(ctx, d, cloud.ParseDetail)
}

// DetailForLAN consumes cloud ssid in addition to the correlated device identity.
// It makes the same read-only detail request; no remote session is allocated.
func (c *Client) DetailForLAN(ctx context.Context, d Device) (cloud.Detail, error) {
	return c.detail(ctx, d, cloud.ParseLANDetail)
}

func (c *Client) detail(ctx context.Context, d Device, parse func([]byte) (cloud.Detail, error)) (result cloud.Detail, err error) {
	finish := diagnostic.Operation(c.logger, "device_detail")
	defer func() { finish(err) }()
	if !validInput(d.Serial) {
		return cloud.Detail{}, environmentInputError("--serial", SerialRule)
	}
	if !validInput(d.Model) {
		return cloud.Detail{}, cloud.ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	info, b, e := c.exchange(ctx, "/v1/my-device/detail", query(d), nil, nil)
	detail, err := parse(b)
	parseErr := err
	// Local client policy: correlate both identity fields
	// before allocation. Correlate decoded identity before interpreting availability. A status
	// belonging to another device is never busy/offline for this request;
	// joining Invalid with Busy would incorrectly keep Busy's higher severity.
	inconsistent := (parseErr == nil || detail.Serial() != "") && (detail.Serial() != d.Serial || detail.Model() != d.Model)
	if inconsistent {
		parseErr = cloud.JoinOutcomes(cloud.ErrInvalid, &cloud.ValidationError{Field: "data", Rule: "requested_device_identity"})
	}
	err = responseError(info, b, parseErr, e)
	if inconsistent {
		err = diagnostic.Wrap("device_detail", "inconsistent server answer: detail identity differs from requested pair", err)
	}
	c.rejected(ctx, "device_detail", info, b, cloud.JoinOutcomes(err, parseErr))
	if err != nil {
		return cloud.Detail{}, err
	}
	if detail.Status() < 0 || detail.Status() > 4 {
		c.unknownDeviceStatus(ctx)
	}
	return detail, nil
}

// Lease is exact ownership of one allocation. Close is idempotent, including
// concurrent calls, and attempts stop once with an independent bounded context.
// It never retries a failed stop. A failed stop cannot guarantee remote release.
type Lease struct {
	client     *Client
	allocation cloud.Allocation
	closing    atomic.Bool
	done       chan struct{}
	err        error
}

func (*Lease) LogValue() slog.Value           { return slog.StringValue("[cloud lease]") }
func (*Lease) Format(s fmt.State, verb rune)  { io.WriteString(s, "[cloud lease]") }
func (l *Lease) Allocation() cloud.Allocation { return l.allocation }

// One bounded stop with the current token, independent of the canceled attempt:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop
func (l *Lease) Close() error {
	if l.closing.CompareAndSwap(false, true) {
		// No lock spans network I/O. Followers join this one bounded operation.
		// Closing also wakes followers if a caller-supplied transport panics.
		defer close(l.done)
		l.err = l.client.stop(l.allocation.ID())
	} else {
		<-l.done
	}
	return l.err
}
func (c *Client) stop(id cloud.Identifier) error {
	ctx, cancel := context.WithTimeout(context.Background(), c.stopTimeout)
	defer cancel()
	return c.stopWithContext(ctx, id)
}

// Acquire performs online-link only; the session should obtain Detail first.
// On failure it stops the identifier decoded from a complete, bounded JSON
// response. On success the caller MUST defer lease.Close immediately,
// even if its attempt was canceled/superseded before delivery. The composed
// session attempt retains that ownership through cancellation and cleanup.
// Retain ownership until handoff; failed response cleanup is a local safety obligation:
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#allocation-online-link
// https://github.com/zmerlynn/seestar-spec/blob/v1/spec/auth/cloud-auth/http-api.md#reports-and-stop
func (c *Client) Acquire(ctx context.Context, d Device) (lease *Lease, err error) {
	defer func() { err = cloud.JoinOutcomes(err) }()
	finish := diagnostic.Operation(c.logger, "allocation")
	defer func() { finish(err) }()
	if !validInput(d.Serial) || !validInput(d.Model) {
		return nil, cloud.ErrConfig
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	requestClient, authErr := c.authorized(ctx)
	if authErr != nil {
		return nil, authErr
	}
	q := query(d)
	q.Set("srcId", c.source)
	var prefix bytes.Buffer
	// Install ownership before reading: even a transport/body panic must release a
	// complete JSON response already captured before the failure.
	defer func() {
		if lease == nil {
			_, ids, _ := cloud.ParseOnline(prefix.Bytes())
			cleanupCtx, stopCancel := context.WithTimeout(context.Background(), c.stopTimeout)
			defer stopCancel()
			var cleanup error
			for _, id := range ids {
				if stopErr := c.stopWithContext(cleanupCtx, id); stopErr != nil {
					cleanup = cloud.JoinOutcomes(cleanup, stopErr)
				}
			}
			if cleanup != nil {
				err = cloud.JoinOutcomes(&AcquireError{Primary: err, Cleanup: cleanup})
			}
		}
	}()
	info, b, e := requestClient.exchangeRaw(ctx, "/v1/device-p2p/online-link", q, nil, &prefix, false, false)
	e = cloud.JoinOutcomes(e, c.remoteStatus(info.status, requestClient.token))
	a, _, parseErr := cloud.ParseOnline(b)
	err = responseError(info, b, parseErr, e)
	c.rejected(ctx, "allocation", info, b, cloud.JoinOutcomes(err, parseErr))
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, cloud.JoinOutcomes(err, ctx.Err())
	}
	return &Lease{client: c, allocation: a, done: make(chan struct{})}, nil
}
func (c *Client) stopWithContext(ctx context.Context, id cloud.Identifier) (err error) {
	finish := diagnostic.Operation(c.logger, "release")
	defer func() { finish(err) }()
	// Preserve the stop body's existing HTML/JS escaping.
	b, _ := json.Marshal(struct {
		ID string `json:"conId"`
	}{id.Value()}, jsontext.EscapeForHTML(true), jsontext.EscapeForJS(true))
	// Stop reads current authorization without entering the credential owner.
	// A lease owns only its conId, never an allocation-to-token binding.
	request := c
	if c.account != nil {
		c.account.expiryMu.Lock()
		token := c.account.currentToken
		c.account.expiryMu.Unlock()
		if token == "" {
			if c.logger != nil {
				c.logger.Warn("release_not_attempted", "attempted", false, "reason", "reauthentication_required")
			}
			return cloud.ErrReauthentication
		}
		request = &Client{logger: c.logger, base: c.base, transport: c.transport, token: token}
	}
	info, body, e := request.exchangeRaw(ctx, "/v1/device-p2p/stop", nil, b, nil, false, false)
	e = cloud.JoinOutcomes(e, c.remoteStatus(info.status, request.token))
	parseErr := cloud.ParseStop(body)
	err = responseError(info, body, parseErr, e)
	c.rejected(ctx, "release", info, body, cloud.JoinOutcomes(err, parseErr))
	return err
}
