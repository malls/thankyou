package printful

import (
	"errors"
	"fmt"
)

// ErrUnauthorized is returned when Printful responds with HTTP 401. The
// caller's job is to translate this to a 502 (the upstream rejected our
// credentials, not the inbound client's).
var ErrUnauthorized = errors.New("printful: unauthorized")

// ErrRateLimited is returned when Printful responds with HTTP 429 and
// the do() helper has already exhausted its single retry. The wrapped
// APIError carries the Retry-After hint when present.
var ErrRateLimited = errors.New("printful: rate limited")

// ErrNotFound is returned for HTTP 404. The store-products idempotency
// flow keys on this — a 404 is the "not yet created" signal, not an error.
var ErrNotFound = errors.New("printful: not found")

// APIError is the typed envelope for any non-success response. StatusCode is
// the raw HTTP status; Message is whatever Printful surfaced in its error
// body (e.g. "Variant ID is invalid"); RawBody is the unparsed bytes for
// debug logging by the caller (handlers must NOT log it indiscriminately).
type APIError struct {
	StatusCode int
	Message    string
	RawBody    []byte
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("printful: api error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("printful: api error %d", e.StatusCode)
}

// Is lets callers do errors.Is(err, ErrUnauthorized) etc. when the error
// chain has been wrapped through APIError.
func (e *APIError) Is(target error) bool {
	switch target {
	case ErrUnauthorized:
		return e.StatusCode == 401
	case ErrNotFound:
		return e.StatusCode == 404
	case ErrRateLimited:
		return e.StatusCode == 429
	}
	return false
}
