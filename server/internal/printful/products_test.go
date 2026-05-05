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

// TestCreateSyncProductHappyPath asserts the POST body shape and the
// response decoding. The v1 envelope nests result under "result"; we
// return SyncProductData (the inner struct).
func TestCreateSyncProductHappyPath(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody CreateSyncProductRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"result":{"id":987,"external_id":"tyb-deadbeef0123","name":"Thank You Bag Tee","sync_variants":[{"external_id":"tyb-S","variant_id":4012,"retail_price":"25.00","files":[{"type":"default","url":"https://example.com/x.png"}]}]}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateSyncProduct(context.Background(), CreateSyncProductRequest{
		SyncProduct: SyncProduct{ExternalID: "tyb-deadbeef0123", Name: "Thank You Bag Tee"},
		SyncVariants: []SyncVariant{{
			ExternalID:  "tyb-S",
			VariantID:   4012,
			RetailPrice: "25.00",
			Files:       []SyncFile{{Type: "default", URL: "https://example.com/x.png"}},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method=%s, want POST", gotMethod)
	}
	if gotPath != "/store/products" {
		t.Errorf("path=%s, want /store/products", gotPath)
	}
	if gotBody.SyncProduct.ExternalID != "tyb-deadbeef0123" {
		t.Errorf("ExternalID did not round-trip: %+v", gotBody)
	}
	if resp.ID != 987 {
		t.Errorf("ID=%d, want 987", resp.ID)
	}
	if resp.ExternalID != "tyb-deadbeef0123" {
		t.Errorf("ExternalID=%q", resp.ExternalID)
	}
}

// TestGetSyncProductByExternalID404IsNotFound checks the typed 404
// translation: 404 from upstream becomes ErrNotFound, the signal the
// orchestrator uses to decide "GET said no, time to POST".
func TestGetSyncProductByExternalID404IsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"error":{"message":"not found"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.GetSyncProductByExternalID(context.Background(), "tyb-doesntexist")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

// TestGetSyncProductByExternalIDHappyPathNestedShape covers the GET response
// shape where result.sync_product wraps the product (the documented v1
// shape for the get-by-external-id endpoint).
func TestGetSyncProductByExternalIDHappyPathNestedShape(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"result":{"sync_product":{"id":555,"external_id":"tyb-abc","name":"Tee"},"sync_variants":[{"external_id":"tyb-S","variant_id":4012,"retail_price":"25.00"}]}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.GetSyncProductByExternalID(context.Background(), "tyb-abc")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if gotPath != "/store/products/@tyb-abc" {
		t.Errorf("path=%s, want /store/products/@tyb-abc", gotPath)
	}
	if resp.ID != 555 {
		t.Errorf("ID=%d, want 555", resp.ID)
	}
	if len(resp.SyncVariants) != 1 || resp.SyncVariants[0].VariantID != 4012 {
		t.Errorf("variants did not unwrap: %+v", resp.SyncVariants)
	}
}

// TestGetSyncProductByExternalIDEmptyIDError is a small contract test:
// an empty external id is a programming error, not a 404.
func TestGetSyncProductByExternalIDEmptyIDError(t *testing.T) {
	c, _ := New(Config{Token: "tok"})
	_, err := c.GetSyncProductByExternalID(context.Background(), "")
	if err == nil {
		t.Fatal("want error for empty id")
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("empty id should not surface as ErrNotFound: %v", err)
	}
}

// TestCreateSyncProduct422SurfacesValidationMessage demonstrates the
// integration point with the handler layer: a 422 from Printful (e.g. invalid
// variant id) carries the upstream message in APIError.Message.
func TestCreateSyncProduct422SurfacesValidationMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(422)
		_, _ = w.Write([]byte(`{"error":{"message":"variant_id is required"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	_, err := c.CreateSyncProduct(context.Background(), CreateSyncProductRequest{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want APIError, got %v", err)
	}
	if apiErr.StatusCode != 422 {
		t.Errorf("status=%d", apiErr.StatusCode)
	}
	if apiErr.Message != "variant_id is required" {
		t.Errorf("message=%q", apiErr.Message)
	}
}
