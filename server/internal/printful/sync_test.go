package printful

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sync/singleflight"
)

// TestExternalIDForHash locks the derivation rule. Same hash -> same id;
// truncates to 12 hex chars + the "tyb-" prefix.
func TestExternalIDForHash(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"deadbeef0123456789abcdef", "tyb-deadbeef0123"},
		{"abc", "tyb-abc"}, // short hash fallback
	}
	for _, tc := range cases {
		if got := ExternalIDForHash(tc.in); got != tc.want {
			t.Errorf("ExternalIDForHash(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestBuildSyncProductRequestUsesCatalog confirms the catalog-driven size +
// price mapping. A variant id present in DefaultVariants gets that size's
// label and price; one absent falls back to a numeric suffix and the
// DefaultRetailPrice.
func TestBuildSyncProductRequestUsesCatalog(t *testing.T) {
	if len(DefaultVariants) == 0 {
		t.Fatal("DefaultVariants empty; test cannot run")
	}
	// Use a synthetic catalog row so the test doesn't depend on whether the
	// human filled in the placeholder VariantID = 0 ids yet.
	old := DefaultVariants
	t.Cleanup(func() { DefaultVariants = old })
	DefaultVariants = []DefaultVariant{
		{Size: "M", VariantID: 9999, RetailPrice: "30.00"},
	}

	req := BuildSyncProductRequest("tyb-test1234", "https://x.example.com/x.png", []int{9999, 1234})
	if req.SyncProduct.ExternalID != "tyb-test1234" {
		t.Errorf("parent external_id=%q", req.SyncProduct.ExternalID)
	}
	if len(req.SyncVariants) != 2 {
		t.Fatalf("variants=%d, want 2", len(req.SyncVariants))
	}
	// First variant: catalog hit
	v0 := req.SyncVariants[0]
	if v0.VariantID != 9999 {
		t.Errorf("v0.VariantID=%d", v0.VariantID)
	}
	if v0.ExternalID != "tyb-test1234-M" {
		t.Errorf("v0.ExternalID=%q", v0.ExternalID)
	}
	if v0.RetailPrice != "30.00" {
		t.Errorf("v0.RetailPrice=%q", v0.RetailPrice)
	}
	// Second variant: catalog miss -> numeric suffix + DefaultRetailPrice
	v1 := req.SyncVariants[1]
	if v1.ExternalID != "tyb-test1234-1234" {
		t.Errorf("v1.ExternalID=%q (want numeric suffix fallback)", v1.ExternalID)
	}
	if v1.RetailPrice != DefaultRetailPrice {
		t.Errorf("v1.RetailPrice=%q, want fallback %q", v1.RetailPrice, DefaultRetailPrice)
	}
}

// TestCreateOrFetchSyncProductGoesPostOn404 covers the create path: GET
// returns 404 -> we POST to /store/products and return the created product.
func TestCreateOrFetchSyncProductGoesPostOn404(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			gets.Add(1)
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":{"message":"not found"}}`)
		case r.Method == "POST" && r.URL.Path == "/store/products":
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":200,"result":{"id":42,"external_id":"tyb-x","name":"Tee"}}`)
		}
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateOrFetchSyncProduct(context.Background(), nil, "tyb-x", "https://example.com/x.png", []int{9999})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("ID=%d, want 42", resp.ID)
	}
	if gets.Load() != 1 {
		t.Errorf("GETs=%d, want 1", gets.Load())
	}
	if posts.Load() != 1 {
		t.Errorf("POSTs=%d, want 1", posts.Load())
	}
}

// TestCreateOrFetchSyncProductReusesExisting covers the GET-200 path: the
// product already exists -> we don't POST.
func TestCreateOrFetchSyncProductReusesExisting(t *testing.T) {
	var gets, posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/store/products/@"):
			gets.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":200,"result":{"sync_product":{"id":42,"external_id":"tyb-x","name":"Tee"}}}`)
		case r.Method == "POST" && r.URL.Path == "/store/products":
			posts.Add(1)
			w.WriteHeader(500)
			_, _ = io.WriteString(w, `{}`)
		}
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	sf := &singleflight.Group{}
	resp, err := c.CreateOrFetchSyncProduct(context.Background(), sf, "tyb-x", "https://example.com/x.png", []int{9999})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("ID=%d, want 42", resp.ID)
	}
	if posts.Load() != 0 {
		t.Errorf("POSTs=%d, want 0 (existing product should not re-create)", posts.Load())
	}
}
