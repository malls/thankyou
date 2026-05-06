package httpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/forrestalmasi/thankyou/server/internal/files"
	"github.com/forrestalmasi/thankyou/server/internal/printful"
	"github.com/forrestalmasi/thankyou/server/internal/render"
)

// stubPrintful captures all requests the handler makes to Printful and lets
// each test pre-program the response per (method, path) tuple. It's shared
// across TestCreateTShirt* and TestCreateMockupOnly*.
type stubPrintful struct {
	srv *httptest.Server

	mockupPosts   atomic.Int32
	mockupGets    atomic.Int32
	productPosts  atomic.Int32
	productGets   atomic.Int32

	// pre-programmed responses
	mockupPostStatus  int
	mockupPostBody    string
	mockupGetStatus   int
	mockupGetBody     string
	productGetStatus  int
	productGetBody    string
	productPostStatus int
	productPostBody   string
}

func newStubPrintful(t *testing.T) *stubPrintful {
	t.Helper()
	s := &stubPrintful{
		mockupPostStatus:  200,
		mockupPostBody:    `{"data":{"id":12345,"status":"pending"}}`,
		mockupGetStatus:   200,
		mockupGetBody:     `{"data":{"id":12345,"status":"completed","catalog_variant_mockups":[{"catalog_variant_id":4012,"mockups":[{"placement":"front","mockup_url":"https://cdn.example.com/m.png"}]}]}}`,
		productGetStatus:  404,
		productGetBody:    `{"error":{"message":"not found"}}`,
		productPostStatus: 200,
		productPostBody:   `{"code":200,"result":{"id":987,"external_id":"tyb-deadbeef0123","name":"Thank You Bag Tee"}}`,
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.srv.Close)
	return s
}

func (s *stubPrintful) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && r.URL.Path == "/v2/mockup-tasks":
		s.mockupPosts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.mockupPostStatus)
		_, _ = io.WriteString(w, s.mockupPostBody)

	case r.Method == "GET" && r.URL.Path == "/v2/mockup-tasks":
		s.mockupGets.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.mockupGetStatus)
		_, _ = io.WriteString(w, s.mockupGetBody)

	case r.Method == "POST" && r.URL.Path == "/store/products":
		s.productPosts.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.productPostStatus)
		_, _ = io.WriteString(w, s.productPostBody)

	case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
		s.productGets.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.productGetStatus)
		_, _ = io.WriteString(w, s.productGetBody)

	default:
		w.WriteHeader(404)
	}
}

// newTestHandlers wires Handlers with a real renderer and store rooted at
// t.TempDir(), and (optionally) a printful.Client pointed at the stub.
func newTestHandlers(t *testing.T, stub *stubPrintful) *Handlers {
	t.Helper()
	store, err := files.New(t.TempDir())
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	rdr, err := render.NewRenderer(context.Background())
	if err != nil {
		t.Fatalf("render.NewRenderer: %v", err)
	}
	t.Cleanup(func() { _ = rdr.Close() })

	h := &Handlers{
		Renderer:      rdr,
		Store:         store,
		PublicBaseURL: "https://public.example.com",
	}
	if stub != nil {
		c, err := printful.New(printful.Config{
			Token:   "test-token",
			BaseURL: stub.srv.URL,
		})
		if err != nil {
			t.Fatalf("printful.New: %v", err)
		}
		h.Printful = &PrintfulSetup{
			Client: c,
		}
	}
	return h
}

// TestCreateTShirtHappyPath asserts the full flow: render+save, mockup POST,
// sync-product GET-then-POST (since the stub returns 404 on GET).
func TestCreateTShirtHappyPath(t *testing.T) {
	stub := newStubPrintful(t)
	h := newTestHandlers(t, stub)

	body := `{"text":"FOO","middletext":"BAR"}`
	req := httptest.NewRequest("POST", "/api/printful/products", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateTShirt(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp printfulProductResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.FileID == "" {
		t.Error("FileID empty")
	}
	if resp.SyncProductID != 987 {
		t.Errorf("SyncProductID=%d, want 987", resp.SyncProductID)
	}
	if resp.MockupTaskID != 12345 {
		t.Errorf("MockupTaskID=%d, want 12345", resp.MockupTaskID)
	}
	if !strings.HasPrefix(resp.ExternalID, "tyb-") {
		t.Errorf("ExternalID=%q want tyb- prefix", resp.ExternalID)
	}
	if resp.MockupStatusURL == "" {
		t.Error("MockupStatusURL empty")
	}
	if got := stub.mockupPosts.Load(); got != 1 {
		t.Errorf("mockup POSTs=%d, want 1", got)
	}
	if got := stub.productGets.Load(); got != 1 {
		t.Errorf("product GETs=%d, want 1", got)
	}
	if got := stub.productPosts.Load(); got != 1 {
		t.Errorf("product POSTs=%d, want 1", got)
	}
}

// TestCreateTShirtIdempotencyReusesExisting POSTs twice with the same body.
// The second call should hit GET /store/products/@... and find it (200) and
// skip the POST. Mockup task POSTs are NOT idempotent (V1 acceptance criteria).
func TestCreateTShirtIdempotencyReusesExisting(t *testing.T) {
	stub := newStubPrintful(t)
	// First call: GET 404 → POST creates. Second call: GET 200 → skip POST.
	// Stub only knows one state at a time, so flip it after the first GET hit.
	var getCalls atomic.Int32
	stub.srv.Close()
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/mockup-tasks":
			stub.mockupPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"id":12345,"status":"pending"}}`)
		case r.Method == "POST" && r.URL.Path == "/store/products":
			stub.productPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":200,"result":{"id":987,"external_id":"tyb-x","name":"Tee"}}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			n := getCalls.Add(1)
			stub.productGets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if n == 1 {
				w.WriteHeader(404)
				_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":200,"result":{"sync_product":{"id":987,"external_id":"tyb-x","name":"Tee"}}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(stub.srv.Close)

	h := newTestHandlers(t, stub)
	// Override the client's BaseURL to the new srv URL.
	c, err := printful.New(printful.Config{Token: "test-token", BaseURL: stub.srv.URL})
	if err != nil {
		t.Fatalf("printful.New: %v", err)
	}
	h.Printful.Client = c

	body := `{"text":"FOO","middletext":"BAR"}`
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/api/printful/products", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		h.CreateTShirt(rr, req)
		if rr.Code != 200 {
			t.Fatalf("call %d status=%d body=%s", i, rr.Code, rr.Body.String())
		}
	}
	if got := stub.mockupPosts.Load(); got != 2 {
		t.Errorf("mockup POSTs=%d, want 2 (mockups not idempotent in V1)", got)
	}
	if got := stub.productGets.Load(); got != 2 {
		t.Errorf("product GETs=%d, want 2", got)
	}
	if got := stub.productPosts.Load(); got != 1 {
		t.Errorf("product POSTs=%d, want 1 (idempotent on second call)", got)
	}
}

// TestCreateTShirtPartialFailure asserts the 502 + partial body shape when
// the sync-product POST fails but the mockup succeeds.
func TestCreateTShirtPartialFailure(t *testing.T) {
	stub := newStubPrintful(t)
	stub.srv.Close()
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/v2/mockup-tasks":
			stub.mockupPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":{"id":12345,"status":"pending"}}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			stub.productGets.Add(1)
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
		case r.Method == "POST" && r.URL.Path == "/store/products":
			stub.productPosts.Add(1)
			// Always fail (twice for the do() retry).
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{"error":{"message":"upstream broken"}}`)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(stub.srv.Close)

	h := newTestHandlers(t, stub)
	c, err := printful.New(printful.Config{Token: "test-token", BaseURL: stub.srv.URL})
	if err != nil {
		t.Fatalf("printful.New: %v", err)
	}
	h.Printful.Client = c

	body := `{"text":"FOO","middletext":"BAR"}`
	req := httptest.NewRequest("POST", "/api/printful/products", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateTShirt(rr, req)

	if rr.Code != 502 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp printfulPartialResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Partial.MockupOK {
		t.Error("MockupOK=false, want true")
	}
	if resp.Partial.SyncProductOK {
		t.Error("SyncProductOK=true, want false")
	}
	if resp.Partial.MockupTaskID != 12345 {
		t.Errorf("MockupTaskID=%d", resp.Partial.MockupTaskID)
	}
	if resp.FileID == "" {
		t.Error("FileID missing in partial body")
	}
	if resp.FileURL == "" {
		t.Error("FileURL missing in partial body")
	}
	if resp.Partial.SyncError == "" {
		t.Error("SyncError empty")
	}
}

// TestCreateTShirtUnconfigured503 confirms the 503 body still includes
// file_id and file_url so the UI can show the saved design.
func TestCreateTShirtUnconfigured503(t *testing.T) {
	h := newTestHandlers(t, nil) // no Printful

	body := `{"text":"FOO","middletext":"BAR"}`
	req := httptest.NewRequest("POST", "/api/printful/products", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateTShirt(rr, req)

	if rr.Code != 503 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["error"] != "printful_unconfigured" {
		t.Errorf("error=%v", resp["error"])
	}
	if resp["file_id"] == nil || resp["file_id"].(string) == "" {
		t.Error("file_id missing in 503")
	}
	if resp["file_url"] == nil || !strings.HasPrefix(resp["file_url"].(string), "/api/files/") {
		t.Errorf("file_url=%v", resp["file_url"])
	}
}

// TestMockupStatusValidatesTaskID locks the regex check.
func TestMockupStatusValidatesTaskID(t *testing.T) {
	h := newTestHandlers(t, nil) // doesn't matter for the validation path

	cases := []struct {
		path    string
		wantStatus int
	}{
		{"/api/printful/mockup/123", 503},        // configured-nil → 503 (not 400)
		{"/api/printful/mockup/abc", 400},
		{"/api/printful/mockup/123abc", 400},
		{"/api/printful/mockup/", 400},
		{"/api/printful/mockup/" + strings.Repeat("9", 21), 400}, // > 20 digits
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.path, nil)
			rr := httptest.NewRecorder()
			h.MockupStatus(rr, req)
			if rr.Code != tc.wantStatus {
				t.Errorf("status=%d, want %d (body=%s)", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

// TestMockupStatusUpstreamErrorTo502 confirms an upstream 401 is translated
// to 502, since the inbound client isn't the one missing creds.
func TestMockupStatusUpstreamErrorTo502(t *testing.T) {
	stub := newStubPrintful(t)
	stub.mockupGetStatus = 401
	stub.mockupGetBody = `{"error":{"message":"bad token"}}`
	h := newTestHandlers(t, stub)

	req := httptest.NewRequest("GET", "/api/printful/mockup/12345", nil)
	rr := httptest.NewRecorder()
	h.MockupStatus(rr, req)
	if rr.Code != 502 {
		t.Errorf("status=%d, want 502", rr.Code)
	}
}

// TestMockupStatusHappyPath confirms a completed task round-trips and the
// mockup_url field surfaces.
func TestMockupStatusHappyPath(t *testing.T) {
	stub := newStubPrintful(t)
	h := newTestHandlers(t, stub)

	req := httptest.NewRequest("GET", "/api/printful/mockup/12345", nil)
	rr := httptest.NewRecorder()
	h.MockupStatus(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "completed") {
		t.Errorf("body=%s, want completed", rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "mockup_url") {
		t.Errorf("body=%s, want mockup_url", rr.Body.String())
	}
}

// TestCreateMockupOnlyHappyPath covers the optional standalone route. With
// {text, middletext} it renders+saves+kicks-off-mockup; the response should
// include task_id, status_url, file_url, file_id.
func TestCreateMockupOnlyHappyPath(t *testing.T) {
	stub := newStubPrintful(t)
	h := newTestHandlers(t, stub)

	body := `{"text":"FOO","middletext":"BAR"}`
	req := httptest.NewRequest("POST", "/api/printful/mockup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateMockupOnly(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp printfulMockupResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TaskID != 12345 {
		t.Errorf("TaskID=%d", resp.TaskID)
	}
	if !strings.HasPrefix(resp.StatusURL, "/api/printful/mockup/") {
		t.Errorf("StatusURL=%q", resp.StatusURL)
	}
	if got := stub.mockupPosts.Load(); got != 1 {
		t.Errorf("mockup POSTs=%d, want 1", got)
	}
	if got := stub.productPosts.Load(); got != 0 {
		t.Errorf("product POSTs=%d, want 0 (this route is mockup-only)", got)
	}
}

// TestCreateMockupOnlyByFileID confirms the reuse path: when {file_id} is
// supplied, no render happens but the mockup is still kicked off.
func TestCreateMockupOnlyByFileID(t *testing.T) {
	stub := newStubPrintful(t)
	h := newTestHandlers(t, stub)

	// Pre-populate a file in the store.
	inputs, err := render.Validate("FOO", "BAR", "")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	hash := render.Hash(inputs)
	if _, err := h.Store.SaveDedup(hash, func() ([]byte, error) {
		return []byte("not-a-real-png-but-fine-for-this-test"), nil
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	body := `{"file_id":"` + hash + `"}`
	req := httptest.NewRequest("POST", "/api/printful/mockup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateMockupOnly(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := stub.mockupPosts.Load(); got != 1 {
		t.Errorf("mockup POSTs=%d, want 1", got)
	}
}

// TestCreateMockupOnlyUnconfigured503 confirms the 503 path includes
// file_id+file_url for graceful degradation.
func TestCreateMockupOnlyUnconfigured503(t *testing.T) {
	h := newTestHandlers(t, nil)

	body := `{"text":"FOO","middletext":"BAR"}`
	req := httptest.NewRequest("POST", "/api/printful/mockup", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateMockupOnly(rr, req)

	if rr.Code != 503 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["file_id"] == nil || resp["file_id"].(string) == "" {
		t.Error("file_id missing")
	}
}

// TestExternalIDForHashSmoke locks the handler-side use of the package
// helper: same hash routes to the same id. The deeper coverage of the rule
// (truncation, prefix, short-hash fallback) lives in printful/sync_test.go.
func TestExternalIDForHashSmoke(t *testing.T) {
	if got := printful.ExternalIDForHash("deadbeef0123456789abcdef"); got != "tyb-deadbeef0123" {
		t.Errorf("got %q, want tyb-deadbeef0123", got)
	}
}

// TestPublicFileURLPrefersConfigured covers the URL-construction logic:
// the configured PUBLIC_BASE_URL on Handlers is used verbatim.
func TestPublicFileURLPrefersConfigured(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://configured.example.com"}
	got := h.publicFileURL("abcd")
	want := "https://configured.example.com/api/files/abcd.png"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPublicFileURLIgnoresHostHeader is the regression guard for the original
// vulnerability: even when a request carries a hostile Host: header, the
// helper must use the boot-configured base, not the request. The helper no
// longer takes *http.Request, but we still want this test to lock the
// invariant that the request can't influence the URL.
func TestPublicFileURLIgnoresHostHeader(t *testing.T) {
	h := &Handlers{PublicBaseURL: "https://configured.example.com"}
	// Construct a hostile request to make the intent visible. The helper's
	// signature does not allow it to be consulted, which is precisely the
	// guarantee this test asserts.
	req := httptest.NewRequest("POST", "/api/printful/products", nil)
	req.Host = "evil.example"
	req.Header.Set("X-Forwarded-Proto", "https")
	_ = req // helper takes no request — the hostile header cannot reach it.
	got := h.publicFileURL("abcd")
	want := "https://configured.example.com/api/files/abcd.png"
	if got != want {
		t.Errorf("got %q, want %q (host header must not influence the URL)", got, want)
	}
}

// TestPublicFileURLTrailingSlash asserts both shapes (with and without a
// trailing slash on PUBLIC_BASE_URL) produce identical helper output.
func TestPublicFileURLTrailingSlash(t *testing.T) {
	for _, base := range []string{"https://x.com", "https://x.com/"} {
		h := &Handlers{PublicBaseURL: base}
		got := h.publicFileURL("abcd")
		want := "https://x.com/api/files/abcd.png"
		if got != want {
			t.Errorf("base=%q: got %q, want %q", base, got, want)
		}
	}
}

// TestPublicBaseURLTrailingSlash mirrors the above for the Stripe-side helper.
func TestPublicBaseURLTrailingSlash(t *testing.T) {
	for _, base := range []string{"https://x.com", "https://x.com/"} {
		h := &Handlers{PublicBaseURL: base}
		got := h.publicBaseURL()
		want := "https://x.com"
		if got != want {
			t.Errorf("base=%q: got %q, want %q", base, got, want)
		}
	}
}

// helper: smoke that POST with bad JSON yields 400.
func TestCreateTShirtBadJSON(t *testing.T) {
	h := newTestHandlers(t, nil)
	req := httptest.NewRequest("POST", "/api/printful/products", bytes.NewReader([]byte(`not json`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.CreateTShirt(rr, req)
	if rr.Code != 400 {
		t.Errorf("status=%d, want 400", rr.Code)
	}
}
