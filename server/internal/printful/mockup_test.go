package printful

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestCreateMockupTaskHappyPath confirms the request body shape and the
// response unmarshal. We assert path, method, and that the body decodes
// to the expected request struct.
func TestCreateMockupTaskHappyPath(t *testing.T) {
	var gotPath, gotMethod string
	var gotBody CreateMockupTaskRequest

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":12345,"status":"pending"}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.CreateMockupTask(context.Background(), CreateMockupTaskRequest{
		Format: "png",
		Products: []MockupProduct{{
			Source:            "catalog",
			CatalogProductID:  71,
			CatalogVariantIDs: []int{4012},
			Placements: []Placement{{
				Placement: "front",
				Technique: "dtg",
				Layers:    []Layer{{Type: "file", URL: "https://example.com/x.png"}},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if gotMethod != "POST" {
		t.Errorf("method=%s, want POST", gotMethod)
	}
	if gotPath != "/v2/mockup-tasks" {
		t.Errorf("path=%s, want /v2/mockup-tasks", gotPath)
	}
	if gotBody.Format != "png" || len(gotBody.Products) != 1 {
		t.Errorf("body did not round-trip: %+v", gotBody)
	}
	if gotBody.Products[0].CatalogProductID != 71 {
		t.Errorf("CatalogProductID=%d, want 71", gotBody.Products[0].CatalogProductID)
	}
	if resp.ID != 12345 {
		t.Errorf("ID=%d, want 12345", resp.ID)
	}
	if resp.Status != "pending" {
		t.Errorf("Status=%q, want pending", resp.Status)
	}
}

// TestGetMockupTaskUsesQueryID confirms the GET path encodes the id as a
// query parameter (the v2 endpoint expects ?id=, not /id at the end).
func TestGetMockupTaskUsesQueryID(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.RequestURI()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":99,"status":"completed","catalog_variant_mockups":[{"catalog_variant_id":4012,"mockups":[{"placement":"front","mockup_url":"https://cdn.example.com/m.png"}]}]}}`))
	}))
	defer srv.Close()

	c, _ := New(Config{Token: "tok", BaseURL: srv.URL})
	resp, err := c.GetMockupTask(context.Background(), 99)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !strings.Contains(gotURL, "/v2/mockup-tasks?") {
		t.Errorf("URL=%q, want /v2/mockup-tasks?...", gotURL)
	}
	if !strings.Contains(gotURL, "id=99") {
		t.Errorf("URL=%q, want id=99", gotURL)
	}
	if resp.Status != "completed" {
		t.Errorf("Status=%q, want completed", resp.Status)
	}
	if len(resp.CatalogVariantMockups) != 1 || len(resp.CatalogVariantMockups[0].Mockups) != 1 {
		t.Fatalf("unexpected mockups: %+v", resp.CatalogVariantMockups)
	}
	if got := resp.CatalogVariantMockups[0].Mockups[0].URL(); got != "https://cdn.example.com/m.png" {
		t.Errorf("mockup URL=%q", got)
	}
}

// TestMockupURLFallback covers the placement_url fallback path. Some Printful
// responses use placement_url instead of mockup_url; URL() prefers mockup_url
// then falls back.
func TestMockupURLFallback(t *testing.T) {
	cases := []struct {
		name string
		in   Mockup
		want string
	}{
		{"prefer mockup_url", Mockup{MockupURL: "a", PlacementURL: "b"}, "a"},
		{"fallback placement_url", Mockup{PlacementURL: "b"}, "b"},
		{"neither", Mockup{}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.in.URL(); got != tc.want {
				t.Errorf("URL()=%q, want %q", got, tc.want)
			}
		})
	}
}
