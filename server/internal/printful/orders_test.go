package printful

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCreateOrderHappyPath confirms the request shape and the auto-confirm
// query parameter. Body must round-trip the external_id and recipient mapping.
func TestCreateOrderHappyPath(t *testing.T) {
	var gotURL string
	var gotBody CreateOrderRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.RequestURI()
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":200,"result":{"id":4242,"external_id":"cs_test_abc","status":"pending"}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateOrder(context.Background(), CreateOrderRequest{
		ExternalID: "cs_test_abc",
		Recipient: OrderRecipient{
			Name:        "Test User",
			Address1:    "1 Main St",
			City:        "NYC",
			StateCode:   "NY",
			CountryCode: "US",
			Zip:         "10001",
			Email:       "test@example.com",
		},
		Items: []OrderItem{{SyncVariantID: 9999, Quantity: 1, RetailPrice: "30.00"}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ID != 4242 {
		t.Errorf("ID=%d, want 4242", resp.ID)
	}
	// confirm=true must be on the URL — auto-confirm is the V1 default.
	if want := "/store/orders?confirm=true"; gotURL != want {
		t.Errorf("URL=%q, want %q", gotURL, want)
	}
	if gotBody.ExternalID != "cs_test_abc" {
		t.Errorf("external_id did not round-trip: %+v", gotBody)
	}
	if gotBody.Recipient.CountryCode != "US" {
		t.Errorf("recipient mapping lost data: %+v", gotBody.Recipient)
	}
	if len(gotBody.Items) != 1 || gotBody.Items[0].SyncVariantID != 9999 {
		t.Errorf("items round-trip failed: %+v", gotBody.Items)
	}
}

// TestCreateOrderDuplicateExternalID covers the Printful 409 path. The
// webhook handler relies on this typed error to treat retries as no-ops.
func TestCreateOrderDuplicateExternalID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = io.WriteString(w, `{"error":{"message":"Provided external id is already used"}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateOrder(context.Background(), CreateOrderRequest{ExternalID: "cs_test_abc"})
	if !errors.Is(err, ErrDuplicateExternalID) {
		t.Errorf("want ErrDuplicateExternalID, got %v", err)
	}
}

// TestCreateOrderDuplicateExternalIDOn400 covers the alternate shape: some
// Printful endpoints return 400 with the same "already used" message.
func TestCreateOrderDuplicateExternalIDOn400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"External id is already in use"}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateOrder(context.Background(), CreateOrderRequest{ExternalID: "cs_test_abc"})
	if !errors.Is(err, ErrDuplicateExternalID) {
		t.Errorf("want ErrDuplicateExternalID for 400/already-used, got %v", err)
	}
}

// TestCreateOrderUpstream5xxRetriesOnce mirrors do()'s retry behaviour. The
// webhook handler counts on transient 5xx surfacing as a typed error after
// retries are exhausted, so it can return 5xx and let Stripe retry.
func TestCreateOrderUpstream5xxRetriesOnce(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = io.WriteString(w, `{"error":{"message":"upstream broken"}}`)
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateOrder(context.Background(), CreateOrderRequest{ExternalID: "cs_test_abc"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 500 {
		t.Errorf("want APIError 500, got %v", err)
	}
}
