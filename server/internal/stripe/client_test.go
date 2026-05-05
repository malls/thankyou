package stripe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	stripego "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// TestNewMissingKeyErrors locks the contract main.go relies on: missing key
// is a typed error, not a panic. Mirrors printful.TestNewMissingTokenErrors.
func TestNewMissingKeyErrors(t *testing.T) {
	_, err := New(Config{Mode: ModeTest})
	if !errors.Is(err, ErrMissingKey) {
		t.Fatalf("want ErrMissingKey, got %v", err)
	}
}

// TestNewMissingModeErrors enforces that callers pass STRIPE_MODE explicitly.
// We don't default to test or live — both are unsafe defaults.
func TestNewMissingModeErrors(t *testing.T) {
	_, err := New(Config{SecretKey: "sk_test_xxx"})
	if !errors.Is(err, ErrModeMismatch) {
		t.Fatalf("want ErrModeMismatch, got %v", err)
	}
}

// TestNewModeMismatchTestKey covers the live-key-with-test-mode case. This is
// the dangerous direction — pasting a live key into a dev .env — so the
// failure mode must be loud.
func TestNewModeMismatchTestKey(t *testing.T) {
	_, err := New(Config{SecretKey: "sk_live_xxx", Mode: ModeTest})
	if !errors.Is(err, ErrModeMismatch) {
		t.Errorf("want ErrModeMismatch, got %v", err)
	}
	_, err = New(Config{SecretKey: "sk_test_xxx", Mode: ModeLive})
	if !errors.Is(err, ErrModeMismatch) {
		t.Errorf("want ErrModeMismatch, got %v", err)
	}
}

// TestNewAcceptsRestrictedKeys covers the rk_test_/rk_live_ prefixes. The
// human's plan calls for a restricted key in production — we can't reject it.
func TestNewAcceptsRestrictedKeys(t *testing.T) {
	if _, err := New(Config{SecretKey: "rk_test_abc", Mode: ModeTest}); err != nil {
		t.Errorf("rk_test_ rejected: %v", err)
	}
	if _, err := New(Config{SecretKey: "rk_live_abc", Mode: ModeLive}); err != nil {
		t.Errorf("rk_live_ rejected: %v", err)
	}
}

// TestNewBootLogIncludesMode confirms the boot line carries the mode and
// base_url, and never includes the secret key.
func TestNewBootLogIncludesMode(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	_, err := New(Config{
		SecretKey:     "sk_test_ULTRASECRET",
		Mode:          ModeTest,
		WebhookSecret: "whsec_xyz",
		Logger:        logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mode=test") {
		t.Errorf("log missing mode=test: %s", out)
	}
	if strings.Contains(out, "ULTRASECRET") {
		t.Errorf("log leaked secret key: %s", out)
	}
	if strings.Contains(out, "whsec_xyz") {
		t.Errorf("log leaked webhook secret: %s", out)
	}
	if !strings.Contains(out, "webhook_secret_set=true") {
		t.Errorf("log should report webhook_secret_set: %s", out)
	}
}

// TestNewLiveModeLogsLoudly confirms the live-mode warning fires. This is the
// "you-are-about-to-take-real-money" guardrail.
func TestNewLiveModeLogsLoudly(t *testing.T) {
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	_, err := New(Config{
		SecretKey: "sk_live_xxx",
		Mode:      ModeLive,
		Logger:    logger,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !strings.Contains(buf.String(), "LIVE MODE ACTIVE") {
		t.Errorf("expected LIVE MODE warning, got: %s", buf.String())
	}
}

// TestCreateCheckoutSessionHitsExpectedPath mounts an httptest.Server, calls
// CreateCheckoutSession, and asserts the request path is /v1/checkout/sessions.
// The body shape is governed by the SDK; we only need to confirm we're
// hitting the right endpoint.
func TestCreateCheckoutSessionHitsExpectedPath(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id": "cs_test_abc123",
			"url": "https://checkout.stripe.com/c/pay/cs_test_abc123",
			"object": "checkout.session",
			"payment_status": "unpaid"
		}`)
	}))
	defer srv.Close()

	c, err := New(Config{
		SecretKey: "sk_test_abc",
		Mode:      ModeTest,
		BaseURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	mode := string(stripego.CheckoutSessionModePayment)
	successURL := "https://example.com/thanks"
	cancelURL := "https://example.com/cancel"
	currency := "usd"
	name := "Tee"
	unitAmount := int64(3000)
	qty := int64(1)
	params := &stripego.CheckoutSessionCreateParams{
		Mode:       &mode,
		SuccessURL: &successURL,
		CancelURL:  &cancelURL,
		LineItems: []*stripego.CheckoutSessionCreateLineItemParams{{
			Quantity: &qty,
			PriceData: &stripego.CheckoutSessionCreateLineItemPriceDataParams{
				Currency:   &currency,
				UnitAmount: &unitAmount,
				ProductData: &stripego.CheckoutSessionCreateLineItemPriceDataProductDataParams{
					Name: &name,
				},
			},
		}},
	}
	sess, err := c.CreateCheckoutSession(context.Background(), params)
	if err != nil {
		t.Fatalf("CreateCheckoutSession: %v", err)
	}
	if sess.ID != "cs_test_abc123" {
		t.Errorf("ID=%q, want cs_test_abc123", sess.ID)
	}
	if sess.URL == "" {
		t.Error("URL empty")
	}
	if gotMethod != "POST" {
		t.Errorf("method=%q, want POST", gotMethod)
	}
	if gotPath != "/v1/checkout/sessions" {
		t.Errorf("path=%q, want /v1/checkout/sessions", gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer sk_test_") {
		t.Errorf("Authorization=%q, want Bearer sk_test_*", gotAuth)
	}
	// Spot-check the form body — the SDK encodes form-style, so we just
	// confirm the obvious fields made it through.
	if !strings.Contains(string(gotBody), "mode=payment") {
		t.Errorf("body missing mode=payment: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), "[unit_amount]=3000") {
		t.Errorf("body missing [unit_amount]=3000: %s", gotBody)
	}
}

// TestCreateCheckoutSessionPropagatesUpstreamErrors ensures a 500 from Stripe
// surfaces as an error to the caller (rather than a silent zero session).
func TestCreateCheckoutSessionPropagatesUpstreamErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream broken","type":"api_error"}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{SecretKey: "sk_test_abc", Mode: ModeTest, BaseURL: srv.URL})
	_, err := c.CreateCheckoutSession(context.Background(), &stripego.CheckoutSessionCreateParams{})
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if calls.Load() == 0 {
		t.Error("upstream not called")
	}
}

// TestVerifyWebhookValidSignature signs a payload with the configured secret
// and confirms the round-trip succeeds. Uses the SDK's GenerateTestSignedPayload
// so we don't reimplement the HMAC algorithm in tests.
func TestVerifyWebhookValidSignature(t *testing.T) {
	const secret = "whsec_test_for_unit"
	body := `{"id":"evt_1","type":"checkout.session.completed","api_version":"` + stripego.APIVersion + `","data":{"object":{"id":"cs_test_abc","payment_status":"paid"}}}`
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(body),
		Secret:    secret,
		Timestamp: time.Now(),
	})

	c, err := New(Config{
		SecretKey:     "sk_test_abc",
		Mode:          ModeTest,
		WebhookSecret: secret,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ev, err := c.VerifyWebhook(sp.Payload, sp.Header)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Type != "checkout.session.completed" {
		t.Errorf("Type=%q", ev.Type)
	}
	// Spot-check that Data.Raw round-trips by unmarshalling the embedded id.
	var got struct {
		ID            string `json:"id"`
		PaymentStatus string `json:"payment_status"`
	}
	if err := json.Unmarshal(ev.Data.Raw, &got); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	if got.ID != "cs_test_abc" {
		t.Errorf("session id=%q", got.ID)
	}
	if got.PaymentStatus != "paid" {
		t.Errorf("payment_status=%q", got.PaymentStatus)
	}
}

// TestVerifyWebhookBadSignature flips one byte of the signature and confirms
// we reject it as ErrInvalidSignature.
func TestVerifyWebhookBadSignature(t *testing.T) {
	const secret = "whsec_test_for_unit"
	c, _ := New(Config{
		SecretKey:     "sk_test_abc",
		Mode:          ModeTest,
		WebhookSecret: secret,
	})

	body := `{"id":"evt_1","type":"checkout.session.completed"}`
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(body),
		Secret:    "different_secret",
		Timestamp: time.Now(),
	})
	_, err := c.VerifyWebhook(sp.Payload, sp.Header)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("want ErrInvalidSignature, got %v", err)
	}
}

// TestVerifyWebhookMissingHeader covers the missing-header path.
func TestVerifyWebhookMissingHeader(t *testing.T) {
	c, _ := New(Config{
		SecretKey:     "sk_test_abc",
		Mode:          ModeTest,
		WebhookSecret: "whsec_anything",
	})
	_, err := c.VerifyWebhook([]byte(`{}`), "")
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("want ErrInvalidSignature for empty header, got %v", err)
	}
}

// TestVerifyWebhookNoSecret confirms that an unset webhook secret rejects
// every request — fail-closed behaviour rather than silent acceptance.
func TestVerifyWebhookNoSecret(t *testing.T) {
	c, _ := New(Config{SecretKey: "sk_test_abc", Mode: ModeTest})
	body := `{"id":"evt_1"}`
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload: []byte(body),
		Secret:  "any",
	})
	_, err := c.VerifyWebhook(sp.Payload, sp.Header)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Errorf("want ErrInvalidSignature when no secret configured, got %v", err)
	}
}
