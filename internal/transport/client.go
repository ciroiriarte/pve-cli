package transport

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/time/rate"

	"github.com/ciroiriarte/pve-cli/internal/auth"
	"github.com/ciroiriarte/pve-cli/internal/protocol"
)

// Options configures a Client.
type Options struct {
	BaseURL    string // e.g. https://pve1.example:8006
	Auth       auth.Provider
	TLS        TLSConfig
	Timeout    time.Duration // per-request timeout (default 30s)
	MaxRetries int           // retries for idempotent requests (default 3)
	Debug      bool          // log request/response metadata to stderr
	UserAgent  string
	RateQPS    float64 // client-side rate limit in requests/sec; <=0 disables
	Burst      int     // token-bucket burst; defaults to max(1, ceil(RateQPS))
}

// Client is the base HTTP client. It is safe for concurrent use.
type Client struct {
	base       *url.URL
	hc         *http.Client
	auth       auth.Provider
	maxRetries int
	debug      bool
	userAgent  string
	limiter    *rate.Limiter
}

// Request is a single API call.
type Request struct {
	Method string
	// Path is relative to /api2/json, e.g. "/cluster/resources".
	Path  string
	Query url.Values
	// Form holds application/x-www-form-urlencoded params for write methods,
	// matching how the Proxmox API expects input.
	Form url.Values
}

// New builds a Client from Options.
func New(opt Options) (*Client, error) {
	if opt.BaseURL == "" {
		return nil, fmt.Errorf("transport: BaseURL is required")
	}
	u, err := url.Parse(strings.TrimRight(opt.BaseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("transport: invalid BaseURL: %w", err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("transport: BaseURL must be an absolute https URL, got %q", opt.BaseURL)
	}
	tlsCfg, err := opt.TLS.build()
	if err != nil {
		return nil, err
	}
	timeout := opt.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	retries := opt.MaxRetries
	if retries == 0 {
		retries = 3
	}
	ua := opt.UserAgent
	if ua == "" {
		ua = "pve-cli"
	}
	return &Client{
		base: u,
		hc: &http.Client{
			Timeout:   timeout,
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
		},
		auth:       opt.Auth,
		maxRetries: retries,
		debug:      opt.Debug,
		userAgent:  ua,
		limiter:    newLimiter(opt.RateQPS, opt.Burst),
	}, nil
}

// newLimiter builds a token-bucket limiter, or nil when rate limiting is
// disabled (qps <= 0).
func newLimiter(qps float64, burst int) *rate.Limiter {
	if qps <= 0 {
		return nil
	}
	if burst < 1 {
		burst = int(math.Ceil(qps))
		if burst < 1 {
			burst = 1
		}
	}
	return rate.NewLimiter(rate.Limit(qps), burst)
}

// Do executes req, decoding the success envelope's data into out (which may be
// nil). It retries idempotent requests on transient failures.
func (c *Client) Do(ctx context.Context, req *Request, out any) error {
	body, err := c.doRaw(ctx, req)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return protocol.DecodeData(body, out)
}

// DoRaw executes req and returns the raw response body (used by `pc api`).
func (c *Client) DoRaw(ctx context.Context, req *Request) ([]byte, error) {
	return c.doRaw(ctx, req)
}

func (c *Client) doRaw(ctx context.Context, req *Request) ([]byte, error) {
	idempotent := isIdempotent(req.Method)
	attempts := 1
	if idempotent {
		attempts = c.maxRetries + 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff(attempt)):
			}
		}

		if c.limiter != nil {
			if err := c.limiter.Wait(ctx); err != nil {
				return nil, &protocol.APIError{Kind: protocol.KindTransport, Message: err.Error()}
			}
		}

		body, status, transErr := c.attempt(ctx, req)
		if transErr != nil {
			lastErr = &protocol.APIError{Kind: protocol.KindTransport, Message: transErr.Error()}
			continue // transport errors are always retryable for idempotent reqs
		}
		if status >= 200 && status < 300 {
			return body, nil
		}
		apiErr := protocol.DecodeError(status, body)
		if idempotent && retryableStatus(status) {
			lastErr = apiErr
			continue
		}
		return nil, apiErr
	}
	return nil, lastErr
}

// attempt performs a single HTTP round-trip.
func (c *Client) attempt(ctx context.Context, req *Request) (body []byte, status int, err error) {
	u := *c.base
	u.Path = "/api2/json" + req.Path
	if len(req.Query) > 0 {
		u.RawQuery = req.Query.Encode()
	}

	var reqBody io.Reader
	write := !isIdempotent(req.Method)
	if write && len(req.Form) > 0 {
		reqBody = strings.NewReader(req.Form.Encode())
	}

	httpReq, err := http.NewRequestWithContext(ctx, req.Method, u.String(), reqBody)
	if err != nil {
		return nil, 0, err
	}
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set("User-Agent", c.userAgent)
	if write && len(req.Form) > 0 {
		httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.auth != nil {
		if err := c.auth.Apply(httpReq, write); err != nil {
			return nil, 0, fmt.Errorf("apply auth: %w", err)
		}
	}

	if c.debug {
		fmt.Fprintf(stderr, "[pc] %s %s\n", httpReq.Method, u.String())
	}

	resp, err := c.hc.Do(httpReq)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if c.debug {
		fmt.Fprintf(stderr, "[pc] -> %d (%d bytes)\n", resp.StatusCode, len(b))
	}
	return b, resp.StatusCode, nil
}

// isIdempotent reports whether method may be safely retried.
func isIdempotent(method string) bool {
	switch strings.ToUpper(method) {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	default:
		return false
	}
}

// retryableStatus reports whether an HTTP status warrants a retry.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return true
	default:
		return false
	}
}

// backoff returns an exponential delay with jitter for the given attempt (>=1).
func backoff(attempt int) time.Duration {
	base := time.Duration(math.Pow(2, float64(attempt-1))) * 200 * time.Millisecond
	if base > 5*time.Second {
		base = 5 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(base/2 + 1)))
	return base + jitter
}
