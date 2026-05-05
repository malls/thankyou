package printful

import (
	"context"
	"net/url"
	"strconv"
)

// CreateMockupTaskRequest is the body for POST /v2/mockup-tasks. Layered as
// per Printful's documented v2 shape: top-level format + products[] where
// each product references a catalog product/variant set and a list of
// placements (front/back/etc.) with the design layers.
type CreateMockupTaskRequest struct {
	Format   string          `json:"format"` // "png"
	Products []MockupProduct `json:"products"`
}

// MockupProduct is one entry in CreateMockupTaskRequest.Products. The plan
// pins source="catalog"; off-catalog products are out of scope for V1.
type MockupProduct struct {
	Source            string      `json:"source"` // "catalog"
	CatalogProductID  int         `json:"catalog_product_id"`
	CatalogVariantIDs []int       `json:"catalog_variant_ids"`
	Placements        []Placement `json:"placements"`
}

// Placement is the print location on the garment. For the V1 tee we only
// use "front" with technique "dtg".
type Placement struct {
	Placement string  `json:"placement"` // "front"
	Technique string  `json:"technique"` // "dtg"
	Layers    []Layer `json:"layers"`
}

// Layer references the print file. Type is "file" and URL is the public
// HTTPS URL of the PNG (Printful GETs it server-side).
type Layer struct {
	Type string `json:"type"` // "file"
	URL  string `json:"url"`
}

// CreateMockupTaskResponse mirrors what Printful returns on POST and GET
// for a mockup task. Status is "pending" until processing completes; on
// completion CatalogVariantMockups is populated with mockup URLs.
type CreateMockupTaskResponse struct {
	ID                    int64                    `json:"id"`
	Status                string                   `json:"status"`
	CatalogVariantMockups []CatalogVariantMockup   `json:"catalog_variant_mockups,omitempty"`
	FailureReasons        []string                 `json:"failure_reasons,omitempty"`
	// Printful's v2 envelope wraps the actual payload under "data". Both
	// ID and Status are duplicated here for compatibility with both flat
	// and wrapped responses; see decodeMockupResponse below.
}

// CatalogVariantMockup is one entry in the variant->mockups[] structure.
type CatalogVariantMockup struct {
	CatalogVariantID int      `json:"catalog_variant_id"`
	Mockups          []Mockup `json:"mockups"`
}

// Mockup is a single rendered preview URL from Printful for a given variant
// and placement. The v2 schema documents `mockup_url`; older flows use
// `placement_url`. We expose both raw fields and a convenience accessor.
type Mockup struct {
	Placement    string `json:"placement,omitempty"`
	MockupURL    string `json:"mockup_url,omitempty"`
	PlacementURL string `json:"placement_url,omitempty"`
}

// URL returns the mockup's image URL, preferring mockup_url then
// placement_url. Returns empty string when neither field is set (which
// would only happen if Printful changes their schema again).
func (m Mockup) URL() string {
	if m.MockupURL != "" {
		return m.MockupURL
	}
	return m.PlacementURL
}

// mockupTaskV2Envelope is the documented v2 wrapper for both POST and GET
// responses on /v2/mockup-tasks: { "data": { ...task... } }. We decode into
// this and unwrap so callers see the flat task struct.
type mockupTaskV2Envelope struct {
	Data CreateMockupTaskResponse `json:"data"`
}

// CreateMockupTask submits a new mockup-task request and returns the task
// (with status="pending" on the happy path). Caller polls GetMockupTask
// until status transitions to "completed" or "failed".
func (c *Client) CreateMockupTask(ctx context.Context, req CreateMockupTaskRequest) (CreateMockupTaskResponse, error) {
	var env mockupTaskV2Envelope
	if err := c.do(ctx, "POST", "/v2/mockup-tasks", req, &env); err != nil {
		return CreateMockupTaskResponse{}, err
	}
	return env.Data, nil
}

// GetMockupTask polls a previously-created task. Printful's v2 GET takes
// the id as a query parameter (?id=...) rather than a path segment.
func (c *Client) GetMockupTask(ctx context.Context, taskID int64) (CreateMockupTaskResponse, error) {
	q := url.Values{}
	q.Set("id", strconv.FormatInt(taskID, 10))
	var env mockupTaskV2Envelope
	if err := c.do(ctx, "GET", "/v2/mockup-tasks?"+q.Encode(), nil, &env); err != nil {
		return CreateMockupTaskResponse{}, err
	}
	return env.Data, nil
}
