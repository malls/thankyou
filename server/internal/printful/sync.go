package printful

import (
	"context"
	"errors"
	"strconv"

	"golang.org/x/sync/singleflight"
)

// ExternalIDPrefix prefixes every external_id we mint. "tyb-" makes the ids
// greppable in the Printful dashboard. Length 4 keeps room for the 12-char
// hash suffix under Printful's 32-char external_id cap.
const ExternalIDPrefix = "tyb-"

// ExternalIDForHash derives the deterministic external_id from the design
// hash. 12 hex chars = 48 bits, collision-resistant for prototype traffic.
// Same hash -> same id, so two consumers (the existing /api/printful/products
// route and the new /api/checkout/start route) compute identical ids without
// coordinating.
func ExternalIDForHash(hash string) string {
	if len(hash) < 12 {
		return ExternalIDPrefix + hash
	}
	return ExternalIDPrefix + hash[:12]
}

// BuildSyncProductRequest constructs the v1 sync-product body for a given
// design + variant set. Each variant gets a unique external_id derived from
// the parent + size suffix; missing-from-catalog variant ids fall back to a
// numeric suffix and DefaultRetailPrice.
//
// The handler-layer helpers used to live in httpserver/printful_handlers.go;
// extracting them here lets the new /api/checkout/start handler reuse the
// same shape without copy-paste.
func BuildSyncProductRequest(externalID, fileURL string, variantIDs []int) CreateSyncProductRequest {
	idToSize := map[int]string{}
	idToPrice := map[int]string{}
	for _, dv := range DefaultVariants {
		idToSize[dv.VariantID] = dv.Size
		idToPrice[dv.VariantID] = dv.RetailPrice
	}

	variants := make([]SyncVariant, 0, len(variantIDs))
	for _, vid := range variantIDs {
		size, ok := idToSize[vid]
		if !ok {
			size = strconv.Itoa(vid)
		}
		price, ok := idToPrice[vid]
		if !ok {
			price = DefaultRetailPrice
		}
		variants = append(variants, SyncVariant{
			ExternalID:  externalID + "-" + size,
			VariantID:   vid,
			RetailPrice: price,
			Files: []SyncFile{{
				Type: "default",
				URL:  fileURL,
			}},
		})
	}
	return CreateSyncProductRequest{
		SyncProduct: SyncProduct{
			ExternalID: externalID,
			Name:       DefaultProductName,
			Thumbnail:  fileURL,
		},
		SyncVariants: variants,
	}
}

// CreateOrFetchSyncProduct implements the GET-then-POST-if-404 idempotency
// flow. The supplied singleflight collapses concurrent identical creates so
// the race-on-404 path doesn't double-create. Returns the existing product
// when GET succeeds; otherwise creates and returns the new one.
//
// Callers that need their own dedup boundary should pass a per-handler
// singleflight; passing the zero value (singleflight.Group{}) is fine for
// callers that don't expect concurrent identical creates.
func (c *Client) CreateOrFetchSyncProduct(ctx context.Context, sf *singleflight.Group, externalID, fileURL string, variantIDs []int) (SyncProductData, error) {
	do := func() (any, error) {
		existing, err := c.GetSyncProductByExternalID(ctx, externalID)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, ErrNotFound) {
			return SyncProductData{}, err
		}
		req := BuildSyncProductRequest(externalID, fileURL, variantIDs)
		return c.CreateSyncProduct(ctx, req)
	}
	if sf == nil {
		v, err := do()
		if err != nil {
			return SyncProductData{}, err
		}
		return v.(SyncProductData), nil
	}
	v, err, _ := sf.Do(externalID, do)
	if err != nil {
		return SyncProductData{}, err
	}
	return v.(SyncProductData), nil
}
