// Package printful is a thin HTTP client for the Printful REST API. It wraps
// the two endpoint families this prototype needs:
//
//   - v2 mockup tasks  (POST /v2/mockup-tasks, GET /v2/mockup-tasks?id=...)
//   - v1 sync products (POST /store/products, GET /store/products/@{external_id})
//
// The v1/v2 split is real: store-product calls live under /store/... (no /v2
// prefix); mockup calls live under /v2/.... Per-method helpers carry the
// path; the client only stores the host root.
//
// Construction errors when the bearer token is empty so main.go can detect
// missing config at boot and pass nil through to the HTTP layer (which then
// 503s the new routes). The render path still works without a token.
package printful

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// DefaultBaseURL is the production Printful host. Overridable via Config.BaseURL
// for tests (the *_test.go files mount an httptest.Server here).
const DefaultBaseURL = "https://api.printful.com"

// DefaultUserAgent identifies our requests in Printful's logs. Bumping when
// the integration ships — anyone debugging on Printful's side can grep for it.
const DefaultUserAgent = "thankyou-server/0.1"

// DefaultTimeout caps a single Printful request (including the one retry).
// Mockup task creation is fast (< 1s typical); the cap is long enough to
// tolerate a one-off slow response without making a single 502 hold the
// inbound client for a minute.
const DefaultTimeout = 30 * time.Second

// max5xxBackoff is the sleep before the single 5xx retry. Short on purpose —
// Printful 5xxs are rare and the inbound HTTP request is waiting on us.
const max5xxBackoff = 500 * time.Millisecond

// maxRetryAfter caps the sleep we'll honour from a Retry-After header so a
// pathological Printful response can't make us sit on the inbound request
// for minutes. The 429 retry path is defence-in-depth; single-user prototype
// traffic shouldn't hit it.
const maxRetryAfter = 10 * time.Second

// Client talks to Printful. Construct once via New, share across goroutines
// (http.Client is safe for concurrent use; the fields here are read-only).
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
	storeID    string
	userAgent  string
	logger     *log.Logger
}

// Config bundles the constructor arguments. Token is required; everything
// else has a sane default.
type Config struct {
	// Token is the Printful bearer token. Required — New returns an error
	// if empty so the caller can decide whether to nil-pass the client
	// (graceful 503) or fatal-fail the boot.
	Token string

	// StoreID is the optional X-PF-Store-Id header. Required for
	// account-level tokens (which can talk to multiple stores); ignored
	// when empty (the more common store-level-token case).
	StoreID string

	// BaseURL overrides the upstream host. Tests set this to the URL of
	// an httptest.Server. Empty means use DefaultBaseURL.
	BaseURL string

	// Timeout caps a single request. Empty means use DefaultTimeout.
	Timeout time.Duration

	// HTTPClient lets the caller supply a pre-configured *http.Client. When
	// nil, New constructs one with the configured timeout. Tests use this
	// to inject a custom Transport.
	HTTPClient *http.Client

	// UserAgent overrides the User-Agent header. Empty means use
	// DefaultUserAgent.
	UserAgent string

	// Logger receives request-line breadcrumbs (endpoint, status,
	// latency, retries). Nil disables logging. Token and full bodies are
	// NEVER logged — see do() for what fields are emitted.
	Logger *log.Logger
}

// ErrMissingToken is returned by New when Config.Token is empty. main.go
// uses errors.Is(err, ErrMissingToken) to distinguish "configure me later"
// from "actually broken."
var ErrMissingToken = errors.New("printful: missing token")

// New constructs a Client. Returns ErrMissingToken when cfg.Token is empty.
func New(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrMissingToken
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: timeout}
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	ua := cfg.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}

	return &Client{
		httpClient: hc,
		baseURL:    baseURL,
		token:      cfg.Token,
		storeID:    cfg.StoreID,
		userAgent:  ua,
		logger:     cfg.Logger,
	}, nil
}

// BaseURL returns the configured host root. Useful for tests and for the
// rare debug log line that wants to confirm we're hitting the right place.
func (c *Client) BaseURL() string { return c.baseURL }

// do executes a single Printful request with the standard headers, handling
// 429 (retry-once-on-Retry-After) and 5xx (retry-once-after-backoff). On 2xx
// the response body is decoded into `out` if non-nil. On non-2xx the body
// is returned as a typed error (ErrUnauthorized, ErrNotFound, ErrRateLimited
// after exhaustion, or APIError).
//
// `body` may be nil for GET; otherwise it must be a JSON-marshallable value.
//
// `out` may be nil if the caller doesn't need the response body decoded.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("printful: marshal body: %w", err)
		}
	}

	url := c.baseURL + path

	// We allow up to two attempts: the first plus one retry for 429 (with
	// Retry-After honoured) or 5xx (with a small fixed backoff).
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		start := time.Now()
		req, err := http.NewRequestWithContext(ctx, method, url, bytesReader(bodyBytes))
		if err != nil {
			return fmt.Errorf("printful: build request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", c.userAgent)
		if bodyBytes != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.storeID != "" {
			req.Header.Set("X-PF-Store-Id", c.storeID)
		}

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Network errors (timeout, connection refused) are surfaced
			// without retry — the timeout was the caller's choice.
			c.logRequest(method, path, 0, time.Since(start), attempt, err)
			return fmt.Errorf("printful: %s %s: %w", method, path, err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			c.logRequest(method, path, resp.StatusCode, time.Since(start), attempt, readErr)
			return fmt.Errorf("printful: read body: %w", readErr)
		}

		c.logRequest(method, path, resp.StatusCode, time.Since(start), attempt, nil)

		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(respBody, out); err != nil {
				return fmt.Errorf("printful: decode response: %w", err)
			}
			return nil

		case resp.StatusCode == http.StatusUnauthorized:
			return &APIError{StatusCode: 401, Message: parseErrorMessage(respBody), RawBody: respBody}

		case resp.StatusCode == http.StatusNotFound:
			return &APIError{StatusCode: 404, Message: parseErrorMessage(respBody), RawBody: respBody}

		case resp.StatusCode == http.StatusTooManyRequests:
			if attempt == 0 {
				wait := parseRetryAfter(resp.Header.Get("Retry-After"))
				if wait > maxRetryAfter {
					wait = maxRetryAfter
				}
				if wait > 0 {
					select {
					case <-ctx.Done():
						return ctx.Err()
					case <-time.After(wait):
					}
				}
				lastErr = &APIError{StatusCode: 429, Message: parseErrorMessage(respBody), RawBody: respBody}
				continue
			}
			return &APIError{StatusCode: 429, Message: parseErrorMessage(respBody), RawBody: respBody}

		case resp.StatusCode >= 500:
			if attempt == 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(max5xxBackoff):
				}
				lastErr = &APIError{StatusCode: resp.StatusCode, Message: parseErrorMessage(respBody), RawBody: respBody}
				continue
			}
			return &APIError{StatusCode: resp.StatusCode, Message: parseErrorMessage(respBody), RawBody: respBody}

		default:
			// 4xx other than 401/404/429 — typically 422 validation. Surface
			// with the upstream message so the handler can pass it through.
			return &APIError{StatusCode: resp.StatusCode, Message: parseErrorMessage(respBody), RawBody: respBody}
		}
	}

	// Unreachable in practice (the loop always returns), but keep a typed
	// fallback for the linter.
	if lastErr != nil {
		return lastErr
	}
	return errors.New("printful: exhausted retries")
}

// bytesReader returns a fresh *bytes.Reader for each retry attempt; passing
// nil for body avoids setting Content-Type.
func bytesReader(b []byte) io.Reader {
	if b == nil {
		return nil
	}
	return bytes.NewReader(b)
}

// parseRetryAfter accepts the seconds-only form of Retry-After. The HTTP-date
// form is ignored (Printful uses seconds in practice). Returns 0 on parse
// failure so the caller falls back to the default backoff.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	secs, err := strconv.Atoi(h)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// parseErrorMessage extracts a human-readable string from Printful's error
// body. Tries the v1 shape `{"error": {"message": "..."}}`, then the v2
// shape `{"error": "..."}`, then a flat `{"message": "..."}`. Returns ""
// when none match; the APIError still carries the raw body for debug.
func parseErrorMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var v1 struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &v1); err == nil && v1.Error.Message != "" {
		return v1.Error.Message
	}
	var v2 struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &v2); err == nil {
		if v2.Message != "" {
			return v2.Message
		}
		if v2.Error != "" {
			return v2.Error
		}
	}
	return ""
}

// logRequest emits a single structured line per request attempt. Note the
// deliberate omissions: no token, no request body, no response body. Only
// the wire-level metadata you need to triage a stuck request.
func (c *Client) logRequest(method, path string, status int, latency time.Duration, attempt int, err error) {
	if c.logger == nil {
		return
	}
	if err != nil {
		c.logger.Printf("printful: %s %s status=%d latency_ms=%d attempt=%d err=%v",
			method, path, status, latency.Milliseconds(), attempt, err)
		return
	}
	c.logger.Printf("printful: %s %s status=%d latency_ms=%d attempt=%d",
		method, path, status, latency.Milliseconds(), attempt)
}
