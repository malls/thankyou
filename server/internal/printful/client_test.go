package printful

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestNewMissingTokenErrors locks the contract main.go relies on: missing
// token at construct time is a typed error, not a panic.
func TestNewMissingTokenErrors(t *testing.T) {
	_, err := New(Config{})
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("want ErrMissingToken, got %v", err)
	}
}

// TestNewWithTokenSucceeds confirms construction with the minimum config.
func TestNewWithTokenSucceeds(t *testing.T) {
	c, err := New(Config{Token: "tok"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c == nil {
		t.Fatal("nil client")
	}
	if c.BaseURL() != DefaultBaseURL {
		t.Errorf("BaseURL=%q, want default", c.BaseURL())
	}
}

// TestDoSendsAuthAndStoreHeaders confirms the auth headers are attached
// and the bearer token is never written to the test logger.
func TestDoSendsAuthAndStoreHeaders(t *testing.T) {
	var gotAuth, gotStore, gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotStore = r.Header.Get("X-PF-Store-Id")
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":1,"status":"pending"}}`))
	}))
	defer srv.Close()

	c, err := New(Config{Token: "secret-token", StoreID: "42", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_, err = c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{Format: "png"})
	if err != nil {
		t.Fatalf("CreateMockupTask: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization=%q, want Bearer secret-token", gotAuth)
	}
	if gotStore != "42" {
		t.Errorf("X-PF-Store-Id=%q, want 42", gotStore)
	}
	if gotUA != DefaultUserAgent {
		t.Errorf("User-Agent=%q, want %q", gotUA, DefaultUserAgent)
	}
}

// TestDo401Returns401APIError covers the unauthorized path. The do() helper
// returns an APIError with StatusCode==401 which errors.Is matches against
// ErrUnauthorized.
func TestDo401Returns401APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"message":"bad token"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{})
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("errors.Is ErrUnauthorized: %v", err)
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 401 {
		t.Errorf("want APIError with 401, got %v", err)
	}
}

// TestDo404Returns404APIError covers the not-found path. GetSyncProductByExternalID
// special-cases this to ErrNotFound, but the underlying do() returns APIError.
func TestDo404Returns404APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.GetSyncProductByExternalID(context.Background(), "tyb-abcdef")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestDo422SurfacesValidationMessage covers the validation case: invalid
// variant id, etc. The handler layer reads APIError.Message into the 502 body.
func TestDo422SurfacesValidationMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"message":"variant_id is invalid"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateSyncProduct(context.Background(), CreateSyncProductRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %v", err)
	}
	if apiErr.StatusCode != 422 {
		t.Errorf("status=%d, want 422", apiErr.StatusCode)
	}
	if apiErr.Message != "variant_id is invalid" {
		t.Errorf("Message=%q, want pass-through", apiErr.Message)
	}
}

// TestDo429RetriesOnceThenSucceeds covers the rate-limit retry: first call
// returns 429 with Retry-After: 0 (so we don't actually sleep in the test),
// second call returns 200.
func TestDo429RetriesOnceThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":99,"status":"pending"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{Format: "png"})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ID != 99 {
		t.Errorf("ID=%d, want 99", resp.ID)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls=%d, want 2 (initial + 1 retry)", got)
	}
}

// TestDo429TwiceSurfaces locks the no-infinite-retry rule: two consecutive
// 429s surface as ErrRateLimited, not a third attempt.
func TestDo429TwiceSurfaces(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"error":{"message":"still limited"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("want ErrRateLimited, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls=%d, want 2 (no further retries)", got)
	}
}

// TestDo5xxRetriesOnceThenSucceeds covers transient 5xx. First call returns
// 503; second returns 200. Test runtime is bounded by max5xxBackoff (500ms).
func TestDo5xxRetriesOnceThenSucceeds(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream broken"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":7,"status":"pending"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ID != 7 {
		t.Errorf("ID=%d, want 7", resp.ID)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls=%d, want 2", got)
	}
}

// TestDo5xxTwiceSurfaces is the partner: two consecutive 5xx surface as
// APIError, not a third call.
func TestDo5xxTwiceSurfaces(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(502)
		_, _ = w.Write([]byte(`{"error":{"message":"bad gateway"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 502 {
		t.Errorf("want APIError 502, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("calls=%d, want 2", got)
	}
}

// TestDoNetworkTimeoutSurfaces locks the no-retry-on-network rule: a network
// timeout from the underlying http.Client surfaces immediately as a wrapped
// error, not as a retry. We use a listener that accepts but never responds.
func TestDoNetworkTimeoutSurfaces(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	// Accept and hang in a goroutine so the dial succeeds but the response
	// never arrives.
	done := make(chan struct{})
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			defer conn.Close()
			// hold open until test done
			<-done
		}
	}()
	defer close(done)

	c, _ := New(Config{
		Token:   "tok",
		BaseURL: "http://" + ln.Addr().String(),
		Timeout: 50 * time.Millisecond,
	})
	start := time.Now()
	_, err = c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{})
	if err == nil {
		t.Fatal("want timeout error")
	}
	if !strings.Contains(err.Error(), "printful:") {
		t.Errorf("error not wrapped with printful prefix: %v", err)
	}
	// Should NOT have retried — that would push elapsed >> 50ms.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("elapsed %v looks like a retry slept", elapsed)
	}
}

// TestParseRetryAfter exercises the small parser. The HTTP-date form is not
// supported (Printful uses seconds in practice); confirm we return 0 for it.
func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in  string
		out time.Duration
	}{
		{"", 0},
		{"0", 0},
		{"3", 3 * time.Second},
		{"-1", 0},
		{"abc", 0},
		{"Wed, 21 Oct 2015 07:28:00 GMT", 0}, // HTTP-date form intentionally unsupported
	}
	for _, tc := range cases {
		if got := parseRetryAfter(tc.in); got != tc.out {
			t.Errorf("parseRetryAfter(%q)=%v, want %v", tc.in, got, tc.out)
		}
	}
}

// TestParseErrorMessage confirms we extract messages from Printful's known
// error envelope shapes.
func TestParseErrorMessage(t *testing.T) {
	cases := []struct {
		body string
		want string
	}{
		{`{"error":{"message":"v1 shape"}}`, "v1 shape"},
		{`{"error":"v2 string shape"}`, "v2 string shape"},
		{`{"message":"flat shape"}`, "flat shape"},
		{`{}`, ""},
		{``, ""},
	}
	for _, tc := range cases {
		if got := parseErrorMessage([]byte(tc.body)); got != tc.want {
			t.Errorf("parseErrorMessage(%q)=%q, want %q", tc.body, got, tc.want)
		}
	}
}

// TestDoTokenNotInLog locks the security rule: the bearer token never
// appears in the logger output. We capture log output and grep for the
// secret value.
func TestDoTokenNotInLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":1,"status":"pending"}}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	c, _ := New(Config{Token: "ULTRASECRET", BaseURL: srv.URL, Logger: logger})
	_, _ = c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{Format: "png"})

	if strings.Contains(buf.String(), "ULTRASECRET") {
		t.Errorf("logger leaked token: %s", buf.String())
	}
}
