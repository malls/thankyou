package printful

import (
	"context"
	"errors"
	"net/url"
)

// CreateSyncProductRequest is the body for POST /store/products. The v1
// schema nests sync_product (the SKU group) with sync_variants (the
// individual SKUs).
type CreateSyncProductRequest struct {
	SyncProduct  SyncProduct   `json:"sync_product"`
	SyncVariants []SyncVariant `json:"sync_variants"`
}

// SyncProduct is the parent of the variant set. ExternalID is our
// idempotency key — same external_id round-trips on the GET endpoint.
type SyncProduct struct {
	ExternalID string `json:"external_id"`
	Name       string `json:"name"`
	// Thumbnail is the URL of the product preview image. We pass the
	// print-file URL here for V1; a real product image (a mockup) would
	// be better but isn't available until the mockup task completes.
	Thumbnail string `json:"thumbnail,omitempty"`
}

// SyncVariant is one SKU. variant_id is Printful's catalog variant ID
// (size + colour for an apparel item). retail_price is a string per the
// API spec ("25.00"). files[].type is the placement (e.g. "default" or
// "front"); url is the public PNG URL.
type SyncVariant struct {
	ExternalID  string     `json:"external_id"`
	VariantID   int        `json:"variant_id"`
	RetailPrice string     `json:"retail_price"`
	Files       []SyncFile `json:"files"`
}

// SyncFile points at the print PNG. Type "default" means the primary print
// area for the product (front of a tee for Bella+Canvas 3001).
type SyncFile struct {
	Type string `json:"type"` // "default" or "front"
	URL  string `json:"url"`
}

// CreateSyncProductResponse mirrors the v1 envelope. The result block is
// what callers actually want.
type CreateSyncProductResponse struct {
	Code   int             `json:"code"`
	Result SyncProductData `json:"result"`
}

// SyncProductData carries the live ids assigned by Printful. SyncVariants
// here echoes back the variants we sent (with their server-assigned ids).
type SyncProductData struct {
	ID           int64         `json:"id"`
	ExternalID   string        `json:"external_id"`
	Name         string        `json:"name"`
	Thumbnail    string        `json:"thumbnail_url,omitempty"`
	SyncVariants []SyncVariant `json:"sync_variants,omitempty"`
}

// getSyncProductV1Envelope is the v1 envelope for the GET endpoint. The
// shape differs slightly between create (returns just the product) and
// get (returns sync_product + sync_variants split). We accept both.
type getSyncProductV1Envelope struct {
	Code   int `json:"code"`
	Result struct {
		SyncProduct  SyncProductData `json:"sync_product"`
		SyncVariants []SyncVariant   `json:"sync_variants,omitempty"`
		// fallback for create-shaped response on a get path
		ID         int64  `json:"id,omitempty"`
		ExternalID string `json:"external_id,omitempty"`
		Name       string `json:"name,omitempty"`
	} `json:"result"`
}

// CreateSyncProduct creates a new store product. Caller is responsible for
// checking GetSyncProductByExternalID first to avoid duplicate creation
// (Printful's behaviour on duplicate external_id is undocumented; the
// safer pattern is GET-first).
func (c *Client) CreateSyncProduct(ctx context.Context, req CreateSyncProductRequest) (SyncProductData, error) {
	var resp CreateSyncProductResponse
	if err := c.do(ctx, "POST", "/store/products", req, &resp); err != nil {
		return SyncProductData{}, err
	}
	return resp.Result, nil
}

// GetSyncProductByExternalID looks up an existing sync product by the
// caller's external_id. Returns (zero, ErrNotFound) when the product
// hasn't been created yet — that's the signal to POST.
//
// Note the @ prefix: per Printful's v1 docs, /store/products/@{external_id}
// disambiguates an external id from a numeric printful id.
func (c *Client) GetSyncProductByExternalID(ctx context.Context, externalID string) (SyncProductData, error) {
	if externalID == "" {
		return SyncProductData{}, errors.New("printful: empty external id")
	}
	path := "/store/products/@" + url.PathEscape(externalID)
	var env getSyncProductV1Envelope
	err := c.do(ctx, "GET", path, nil, &env)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			return SyncProductData{}, ErrNotFound
		}
		return SyncProductData{}, err
	}
	// Prefer the nested sync_product shape; fall back to flat if Printful
	// returned the create-style envelope.
	if env.Result.SyncProduct.ID != 0 || env.Result.SyncProduct.ExternalID != "" {
		out := env.Result.SyncProduct
		out.SyncVariants = env.Result.SyncVariants
		return out, nil
	}
	return SyncProductData{
		ID:           env.Result.ID,
		ExternalID:   env.Result.ExternalID,
		Name:         env.Result.Name,
		SyncVariants: env.Result.SyncVariants,
	}, nil
}
