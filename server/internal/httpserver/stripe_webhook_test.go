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
	"time"

	stripego "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"

	"github.com/forrestalmasi/thankyou/server/internal/files"
	"github.com/forrestalmasi/thankyou/server/internal/printful"
	"github.com/forrestalmasi/thankyou/server/internal/render"
	tystripe "github.com/forrestalmasi/thankyou/server/internal/stripe"
)

const testWebhookSecret = "whsec_test_for_unit"

// webhookStub bundles the Printful stub + counters so each test can program
// distinct responses for /store/products/@... (sync-product fetch) and
// /store/orders (the order create the webhook triggers).
type webhookStub struct {
	srv *httptest.Server

	productGetStatus  int
	productGetBody    string
	orderPostStatus   int
	orderPostBody     string
	orderPosts        atomic.Int32
	productGets       atomic.Int32
}

func newWebhookStub(t *testing.T) *webhookStub {
	t.Helper()
	s := &webhookStub{
		productGetStatus: 200,
		// Default product carries a sync_variants row with id=555, variant_id=4012
		// matching the metadata the test events ship with.
		productGetBody: `{"code":200,"result":{"sync_product":{"id":987,"external_id":"tyb-x","name":"Tee"},"sync_variants":[{"id":555,"external_id":"tyb-x-M","variant_id":4012,"retail_price":"30.00"}]}}`,
		orderPostStatus: 200,
		orderPostBody:   `{"code":200,"result":{"id":42,"external_id":"cs_test_abc","status":"pending"}}`,
	}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			s.productGets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.productGetStatus)
			_, _ = io.WriteString(w, s.productGetBody)
		case r.Method == "POST" && r.URL.Path == "/store/orders":
			s.orderPosts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.orderPostStatus)
			_, _ = io.WriteString(w, s.orderPostBody)
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// newWebhookHandlers wires Handlers with a Stripe client whose webhook
// secret matches testWebhookSecret and a Printful client pointed at the
// supplied stub.
func newWebhookHandlers(t *testing.T, ws *webhookStub) *Handlers {
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

	pf, err := printful.New(printful.Config{Token: "tok", BaseURL: ws.srv.URL})
	if err != nil {
		t.Fatalf("printful.New: %v", err)
	}
	sc, err := tystripe.New(tystripe.Config{
		SecretKey:     "sk_test_xxx",
		Mode:          tystripe.ModeTest,
		WebhookSecret: testWebhookSecret,
	})
	if err != nil {
		t.Fatalf("tystripe.New: %v", err)
	}
	return &Handlers{
		Renderer: rdr,
		Store:    store,
		Printful: &PrintfulSetup{Client: pf, PublicBaseURL: "https://public.example.com"},
		Stripe:   &StripeSetup{Client: sc},
	}
}

// signWebhookPayload builds a signed Stripe-Signature header for the given
// body. Matches Stripe's documented signing algorithm (HMAC-SHA256 over
// "<unix_ts>.<payload>"). The webhook package's GenerateTestSignedPayload
// gives us this for free.
func signWebhookPayload(t *testing.T, body []byte, ts time.Time, secret string) (signedBody []byte, header string) {
	t.Helper()
	if ts.IsZero() {
		ts = time.Now()
	}
	if secret == "" {
		secret = testWebhookSecret
	}
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   body,
		Secret:    secret,
		Timestamp: ts,
	})
	return sp.Payload, sp.Header
}

// buildEventBody assembles a checkout.session.completed event with the
// supplied session payload. APIVersion is set to the SDK's pinned version
// so signature verification with default IgnoreAPIVersionMismatch=true
// (in our wrapper) doesn't 400 on a benign mismatch.
func buildEventBody(t *testing.T, eventID string, session map[string]any) []byte {
	t.Helper()
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal session: %v", err)
	}
	body, err := json.Marshal(map[string]any{
		"id":          eventID,
		"object":      "event",
		"api_version": stripego.APIVersion,
		"type":        "checkout.session.completed",
		"data":        map[string]any{"object": json.RawMessage(raw)},
	})
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	return body
}

// validSession constructs a minimal-but-complete checkout.session payload
// that should pass the webhook handler's metadata + payment + address
// checks and trigger a CreateOrder call.
func validSession(id string) map[string]any {
	return map[string]any{
		"id":             id,
		"object":         "checkout.session",
		"payment_status": "paid",
		"metadata": map[string]string{
			"sync_product_id": "987",
			"variant_id":      "4012",
			"file_id":         "deadbeef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
			"external_id":     "tyb-x",
		},
		"customer_details": map[string]any{
			"name":  "Test User",
			"email": "test@example.com",
			"phone": "+15551234",
			"address": map[string]any{
				"line1":       "1 Main St",
				"city":        "NYC",
				"country":     "US",
				"postal_code": "10001",
				"state":       "NY",
			},
		},
		"shipping_details": map[string]any{
			"name": "Test User",
			"address": map[string]any{
				"line1":       "1 Main St",
				"city":        "NYC",
				"country":     "US",
				"postal_code": "10001",
				"state":       "NY",
			},
		},
	}
}

// TestStripeWebhookSignatureValid is the happy path: a signed event triggers
// the Printful order POST with the session.id as external_id.
func TestStripeWebhookSignatureValid(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	body := buildEventBody(t, "evt_1", validSession("cs_test_abc"))
	signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := ws.orderPosts.Load(); got != 1 {
		t.Errorf("order POSTs=%d, want 1", got)
	}
	if got := ws.productGets.Load(); got != 1 {
		t.Errorf("product GETs=%d, want 1", got)
	}
}

// TestStripeWebhookSignatureInvalid covers the bad-signature case.
func TestStripeWebhookSignatureInvalid(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	body := buildEventBody(t, "evt_2", validSession("cs_test_bad"))
	// Sign with wrong secret.
	signed, sig := signWebhookPayload(t, body, time.Now(), "whsec_DIFFERENT_secret")

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 400 {
		t.Errorf("status=%d, want 400", rr.Code)
	}
	if got := ws.orderPosts.Load(); got != 0 {
		t.Errorf("order POSTed despite bad signature: %d", got)
	}
}

// TestStripeWebhookIdempotent posts the same signed event twice; only one
// CreateOrder should be triggered.
func TestStripeWebhookIdempotent(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	for i := 0; i < 2; i++ {
		body := buildEventBody(t, "evt_dup", validSession("cs_test_dup"))
		// Re-sign each delivery with a fresh timestamp — Stripe's tolerance
		// is only 5 minutes; same payload signed twice is allowed.
		signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)
		req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
		req.Header.Set("Stripe-Signature", sig)
		rr := httptest.NewRecorder()
		h.StripeWebhook(rr, req)
		if rr.Code != 200 {
			t.Fatalf("call %d status=%d body=%s", i, rr.Code, rr.Body.String())
		}
	}
	if got := ws.orderPosts.Load(); got != 1 {
		t.Errorf("order POSTs=%d, want 1 (idempotent on duplicate session id)", got)
	}
}

// TestStripeWebhookUnknownEventType posts a customer.created event; should
// be 200'd with no Printful side effects.
func TestStripeWebhookUnknownEventType(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	bodyMap := map[string]any{
		"id":          "evt_other",
		"object":      "event",
		"api_version": stripego.APIVersion,
		"type":        "customer.created",
		"data":        map[string]any{"object": json.RawMessage(`{}`)},
	}
	raw, _ := json.Marshal(bodyMap)
	signed, sig := signWebhookPayload(t, raw, time.Now(), testWebhookSecret)

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 200 {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if got := ws.orderPosts.Load(); got != 0 {
		t.Errorf("order POSTed for unknown event type: %d", got)
	}
}

// TestStripeWebhookPaymentNotPaid confirms a session with payment_status
// "unpaid" is acked but not fulfilled.
func TestStripeWebhookPaymentNotPaid(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	sess := validSession("cs_test_unpaid")
	sess["payment_status"] = "unpaid"
	body := buildEventBody(t, "evt_unpaid", sess)
	signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 200 {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if got := ws.orderPosts.Load(); got != 0 {
		t.Errorf("order POSTed despite unpaid: %d", got)
	}
}

// TestStripeWebhookPrintful5xxRetries asserts the retry-friendly behaviour:
// when Printful's CreateOrder returns 5xx, the webhook returns 5xx so Stripe
// will retry the delivery.
func TestStripeWebhookPrintful5xxRetries(t *testing.T) {
	ws := newWebhookStub(t)
	ws.orderPostStatus = 500
	ws.orderPostBody = `{"error":{"message":"upstream broken"}}`
	h := newWebhookHandlers(t, ws)

	body := buildEventBody(t, "evt_5xx", validSession("cs_test_5xx"))
	signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code < 500 {
		t.Errorf("status=%d, want 5xx so Stripe retries", rr.Code)
	}
}

// TestStripeWebhookMissingMetadata: a session without the expected metadata
// keys should be 200'd + alert-logged but never trigger a Printful order.
func TestStripeWebhookMissingMetadata(t *testing.T) {
	ws := newWebhookStub(t)
	h := newWebhookHandlers(t, ws)

	sess := validSession("cs_test_nometa")
	delete(sess, "metadata")
	body := buildEventBody(t, "evt_nometa", sess)
	signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)

	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 200 {
		t.Errorf("status=%d, want 200", rr.Code)
	}
	if got := ws.orderPosts.Load(); got != 0 {
		t.Errorf("order POSTed without metadata: %d", got)
	}
}

// TestStripeWebhookOrderCarriesExternalID checks the external_id Printful
// receives equals the Stripe session id — this is the durable layer of the
// idempotency story.
func TestStripeWebhookOrderCarriesExternalID(t *testing.T) {
	var capturedExternalID string
	ws := &webhookStub{
		productGetStatus: 200,
		productGetBody:   `{"code":200,"result":{"sync_product":{"id":987,"external_id":"tyb-x","name":"Tee"},"sync_variants":[{"id":555,"external_id":"tyb-x-M","variant_id":4012,"retail_price":"30.00"}]}}`,
	}
	ws.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			ws.productGets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, ws.productGetBody)
		case r.Method == "POST" && r.URL.Path == "/store/orders":
			ws.orderPosts.Add(1)
			body, _ := io.ReadAll(r.Body)
			var got printful.CreateOrderRequest
			_ = json.Unmarshal(body, &got)
			capturedExternalID = got.ExternalID
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":200,"result":{"id":42,"external_id":"cs_xyz","status":"pending"}}`)
		}
	}))
	t.Cleanup(ws.srv.Close)
	h := newWebhookHandlers(t, ws)

	body := buildEventBody(t, "evt_carry", validSession("cs_test_carry"))
	signed, sig := signWebhookPayload(t, body, time.Now(), testWebhookSecret)
	req := httptest.NewRequest("POST", "/api/stripe/webhook", bytes.NewReader(signed))
	req.Header.Set("Stripe-Signature", sig)
	rr := httptest.NewRecorder()
	h.StripeWebhook(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	if capturedExternalID != "cs_test_carry" {
		t.Errorf("external_id=%q, want cs_test_carry", capturedExternalID)
	}
}
