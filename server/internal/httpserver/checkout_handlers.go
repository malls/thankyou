package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	stripego "github.com/stripe/stripe-go/v82"

	"github.com/forrestalmasi/thankyou/server/internal/printful"
	"github.com/forrestalmasi/thankyou/server/internal/render"
	tystripe "github.com/forrestalmasi/thankyou/server/internal/stripe"
)

// MaxCheckoutStartBodyBytes caps the request body for /api/checkout/start.
// Same threshold as MaxPrintfulBodyBytes — the body shape is identical plus
// a single integer variant_id field.
const MaxCheckoutStartBodyBytes = 4 << 10 // 4 KiB

// StripeSetup is the optional dependency block. Constructed in main.go when
// STRIPE_SECRET_KEY is present; left nil otherwise (handlers detect the nil
// and 503 the routes). Mirrors the PrintfulSetup pattern verbatim so the
// router wiring stays uniform.
//
// SeenSessions is the in-memory layer of the webhook idempotency story
// (sync.Map keyed by session.id). It catches duplicate deliveries within
// seconds of each other in the same process; the durable layer is
// Printful's external_id check (see printful.ErrDuplicateExternalID).
type StripeSetup struct {
	Client       *tystripe.Client
	SeenSessions sync.Map
	// PriceCentsOverride lets STRIPE_PRICE_USD_CENTS bypass the catalog
	// price for ad-hoc adjustments. Zero means "use the catalog".
	PriceCentsOverride int64
}

// checkoutStartRequest is the body for POST /api/checkout/start. text and
// middletext mirror /api/render and /api/printful/products; variant_id is
// the size-picker selection from the front-end.
type checkoutStartRequest struct {
	Text       string `json:"text"`
	MiddleText string `json:"middletext"`
	Background string `json:"background,omitempty"`
	VariantID  int    `json:"variant_id"`
}

// checkoutStartResponse is the 200 body. The orphan-correlation fields
// (sync_product_id, file_id) are echoed back so the client can show useful
// diagnostics on a stripe_session_failed retry.
type checkoutStartResponse struct {
	CheckoutURL   string `json:"checkout_url"`
	SessionID     string `json:"session_id"`
	SyncProductID int64  `json:"sync_product_id"`
	FileID        string `json:"file_id"`
}

// StartCheckout handles POST /api/checkout/start. The orchestration sequence
// is documented in plans/task_01KQWEW3DBADPSAXZXC5R0XGMX.md §"End-to-end flow":
//
//  1. Validate text/middletext + variant_id against the catalog.
//  2. Render+save the PNG (reuse the existing render path so identical
//     designs round-trip the same file_id).
//  3. Create a Printful sync_product. On any Printful error, return 502
//     with {error:"printful_create_failed"} and DO NOT call Stripe.
//  4. Build a Checkout Session with inline price_data (no pre-created
//     Stripe Products) and call /v1/checkout/sessions. On Stripe error,
//     return 502 with {error:"stripe_session_failed", sync_product_id,
//     file_id} so the operator can correlate the orphan sync_product.
//  5. Return 200 with the checkout URL for the client to redirect to.
//
// 503 paths mirror the Printful pattern: STRIPE_SECRET_KEY unset or
// PRINTFUL_TOKEN unset both surface as 503 with a typed error code so the
// front-end degrades gracefully.
func (h *Handlers) StartCheckout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "POST required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxCheckoutStartBodyBytes)
	defer r.Body.Close()

	var req checkoutStartRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "", err.Error())
		return
	}

	inputs, err := render.Validate(req.Text, req.MiddleText, req.Background)
	if err != nil {
		var ve *render.ValidationError
		if errors.As(err, &ve) {
			writeError(w, http.StatusBadRequest, "validation_failed", ve.Field, ve.Reason)
			return
		}
		writeError(w, http.StatusBadRequest, "validation_failed", "", err.Error())
		return
	}

	// The catalog must contain the requested variant id. Zero is the
	// uninitialised value — reject it explicitly so a client that omits the
	// field gets a 400 instead of a confused 503 ("catalog incomplete").
	if req.VariantID == 0 {
		writeError(w, http.StatusBadRequest, "validation_failed", "variant_id", "variant_id is required")
		return
	}
	if !catalogContainsVariant(req.VariantID) {
		writeError(w, http.StatusBadRequest, "validation_failed", "variant_id", "unknown variant_id")
		return
	}

	// 503 paths: each integration is independently optional. Check Stripe
	// first so a Stripe-unconfigured server doesn't render+save uselessly.
	// Order matches the plan's failure table.
	if h.Stripe == nil || h.Stripe.Client == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "stripe_unconfigured",
			"message": "server is missing STRIPE_SECRET_KEY",
		})
		h.logf("checkout/start: stripe_unconfigured 503")
		return
	}
	if h.Printful == nil || h.Printful.Client == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "printful_unconfigured",
			"message": "server is missing PRINTFUL_TOKEN",
		})
		h.logf("checkout/start: printful_unconfigured 503")
		return
	}
	if !printful.CatalogConfigured() {
		// The placeholder VariantID = 0 catalog rows are flagged in
		// catalog.go; until the human fills them in, end-to-end checkout
		// can't work. Surfacing 503 here (rather than letting Printful
		// reject with 422) gives the front-end a clean error code.
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "variant_catalog_incomplete",
			"message": "Printful variant catalog is missing real ids; see internal/printful/catalog.go",
		})
		h.logf("checkout/start: variant_catalog_incomplete 503")
		return
	}

	hash := render.Hash(inputs)
	if _, err := h.Store.SaveDedup(hash, func() ([]byte, error) {
		return h.Renderer.RenderPNG(r.Context(), inputs)
	}); err != nil {
		h.logf("checkout/start: hash=%s render err=%v", hash, err)
		writeError(w, http.StatusInternalServerError, "render_failed", "", "render failed")
		return
	}
	relativeFileURL := "/api/files/" + hash + ".png"

	publicFileURL := h.publicFileURL(hash)
	externalID := printful.ExternalIDForHash(hash)

	// Create-or-fetch the sync_product. Single-variant request: we only need
	// Stripe to know about the size the customer picked. Other sizes can be
	// added by re-clicking buy with a different variant.
	productResp, err := h.Printful.Client.CreateOrFetchSyncProduct(
		r.Context(),
		&h.Printful.SyncProductSF,
		externalID,
		publicFileURL,
		[]int{req.VariantID},
	)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":    "printful_create_failed",
			"message":  errMessageForClient(err),
			"file_id":  hash,
			"file_url": relativeFileURL,
		})
		h.logf("checkout/start: hash=%s printful err=%v", hash, err)
		return
	}

	// Compute the unit price. Server-side source of truth: the catalog
	// (variant.RetailPrice × 100). STRIPE_PRICE_USD_CENTS overrides for
	// ad-hoc adjustments without re-deploying.
	unitAmount, err := unitAmountCentsFor(req.VariantID, h.Stripe.PriceCentsOverride)
	if err != nil {
		h.logf("checkout/start: hash=%s price_compute err=%v", hash, err)
		writeError(w, http.StatusInternalServerError, "price_compute_failed", "", err.Error())
		return
	}

	publicBase := h.publicBaseURL()
	successURL := publicBase + "/?session_id={CHECKOUT_SESSION_ID}"
	cancelURL := publicBase + "/?canceled=1"

	params := buildCheckoutSessionParams(checkoutSessionInputs{
		FileID:        hash,
		ExternalID:    externalID,
		SyncProductID: productResp.ID,
		VariantID:     req.VariantID,
		UnitAmount:    unitAmount,
		ImageURL:      publicFileURL,
		ProductName:   designSummaryName(inputs),
		SuccessURL:    successURL,
		CancelURL:     cancelURL,
		Countries:     printful.SupportedCountries,
	})

	sess, err := h.Stripe.Client.CreateCheckoutSession(r.Context(), params)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"error":           "stripe_session_failed",
			"message":         err.Error(),
			"sync_product_id": productResp.ID,
			"file_id":         hash,
		})
		h.logf("checkout/start: hash=%s sync_id=%d stripe err=%v", hash, productResp.ID, err)
		return
	}

	resp := checkoutStartResponse{
		CheckoutURL:   sess.URL,
		SessionID:     sess.ID,
		SyncProductID: productResp.ID,
		FileID:        hash,
	}
	writeJSON(w, http.StatusOK, resp)
	h.logf("checkout/start: hash=%s sync_id=%d session_id=%s ok",
		hash, productResp.ID, sess.ID)
}

// catalogContainsVariant reports whether the variant id appears in the
// configured Printful catalog. Zero is never present.
func catalogContainsVariant(id int) bool {
	if id == 0 {
		return false
	}
	for _, dv := range printful.DefaultVariants {
		if dv.VariantID == id {
			return true
		}
	}
	return false
}

// unitAmountCentsFor maps a Printful variant id to the Stripe unit_amount
// in cents. Override is non-zero when STRIPE_PRICE_USD_CENTS is set.
//
// The catalog stores RetailPrice as a string ("30.00") because that's the
// shape Printful expects. We parse it as a float, multiply by 100, and
// round to the nearest cent. Anything that fails to parse surfaces as a
// 500 — the catalog is server-controlled, so a parse failure is a code bug.
func unitAmountCentsFor(variantID int, override int64) (int64, error) {
	if override > 0 {
		return override, nil
	}
	for _, dv := range printful.DefaultVariants {
		if dv.VariantID != variantID {
			continue
		}
		f, err := strconv.ParseFloat(dv.RetailPrice, 64)
		if err != nil {
			return 0, errors.New("invalid catalog retail_price for variant: " + dv.RetailPrice)
		}
		// Round half away from zero — strconv-style.
		cents := int64(f*100 + 0.5)
		if cents <= 0 {
			return 0, errors.New("catalog retail_price computed to non-positive cents")
		}
		return cents, nil
	}
	return 0, errors.New("variant not in catalog")
}

// checkoutSessionInputs bundles the per-request values that go into the
// Stripe Session. Keeping them in a struct (rather than a long argument
// list) makes buildCheckoutSessionParams easy to read and trivially
// re-orderable in tests.
type checkoutSessionInputs struct {
	FileID        string
	ExternalID    string
	SyncProductID int64
	VariantID     int
	UnitAmount    int64
	ImageURL      string
	ProductName   string
	SuccessURL    string
	CancelURL     string
	Countries     []string
}

// buildCheckoutSessionParams constructs the Stripe SDK params struct.
// Pulled out as a free function so tests can assert the shape directly
// without a real HTTP round-trip.
func buildCheckoutSessionParams(in checkoutSessionInputs) *stripego.CheckoutSessionCreateParams {
	mode := string(stripego.CheckoutSessionModePayment)
	currency := "usd"
	qty := int64(1)
	productName := in.ProductName

	// Stripe's product_data.metadata is line-item-scoped — useful for
	// reporting but not surfaced on the webhook. The webhook reads the
	// session-level Metadata block (see below).
	productData := &stripego.CheckoutSessionCreateLineItemPriceDataProductDataParams{
		Name: &productName,
	}
	if in.ImageURL != "" {
		img := in.ImageURL
		productData.Images = []*string{&img}
	}
	productData.AddMetadata("sync_product_id", strconv.FormatInt(in.SyncProductID, 10))
	productData.AddMetadata("variant_id", strconv.Itoa(in.VariantID))
	productData.AddMetadata("file_id", in.FileID)

	priceData := &stripego.CheckoutSessionCreateLineItemPriceDataParams{
		Currency:    &currency,
		UnitAmount:  &in.UnitAmount,
		ProductData: productData,
	}

	phoneEnabled := true
	billing := "auto"

	countriesPtr := make([]*string, 0, len(in.Countries))
	for _, c := range in.Countries {
		c := c
		countriesPtr = append(countriesPtr, &c)
	}

	params := &stripego.CheckoutSessionCreateParams{
		Mode:                     &mode,
		SuccessURL:               &in.SuccessURL,
		CancelURL:                &in.CancelURL,
		BillingAddressCollection: &billing,
		PhoneNumberCollection: &stripego.CheckoutSessionCreatePhoneNumberCollectionParams{
			Enabled: &phoneEnabled,
		},
		ShippingAddressCollection: &stripego.CheckoutSessionCreateShippingAddressCollectionParams{
			AllowedCountries: countriesPtr,
		},
		LineItems: []*stripego.CheckoutSessionCreateLineItemParams{{
			Quantity:  &qty,
			PriceData: priceData,
		}},
	}
	// Session-level metadata is what the webhook handler reads. Keep keys
	// short and stable.
	params.AddMetadata("sync_product_id", strconv.FormatInt(in.SyncProductID, 10))
	params.AddMetadata("variant_id", strconv.Itoa(in.VariantID))
	params.AddMetadata("file_id", in.FileID)
	params.AddMetadata("external_id", in.ExternalID)
	params.AddMetadata("app", "thankyou")
	params.AddMetadata("schema", "v1")

	// Idempotency key: a hash of file_id + variant_id + a 60s window. Stops
	// accidental double-clicks on the same design from creating two Sessions
	// (Stripe re-returns the existing Session for a duplicate idempotency
	// key). The window means a deliberate retry after the user closes the
	// Checkout tab still creates a fresh Session — desirable, because the
	// previous one may have expired.
	key := idempotencyKey(in.FileID, in.VariantID)
	params.SetIdempotencyKey(key)
	// SDK params struct doesn't expose ExpiresAt directly here; we leave
	// Stripe's default (24h) since the buy flow expects same-session redirect.

	return params
}

// idempotencyKey hashes the design + variant + the 60s wall-clock window.
// Concretely: sha256(file_id|variant_id|floor(unix/60))[:32].
//
// We don't include the client IP in the key (Stripe's idempotency-key system
// is server-scoped, not client-scoped); the hash already prevents accidental
// double-create within the same design within 60s.
func idempotencyKey(fileID string, variantID int) string {
	window := time.Now().Unix() / 60
	canonical := fileID + "|" + strconv.Itoa(variantID) + "|" + strconv.FormatInt(window, 10)
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:32]
}

// designSummaryName returns the product name shown on the Stripe Checkout
// page. The customer just designed it on the previous screen, so a short
// "Thank You Bag Tee — <text>" is the most useful label.
func designSummaryName(in render.Inputs) string {
	const max = 64
	parts := []string{printful.DefaultProductName}
	summary := in.MainText
	if in.MiddleText != "" {
		summary = summary + " / " + in.MiddleText
	}
	if summary != "" {
		parts = append(parts, "—", summary)
	}
	name := strings.Join(parts, " ")
	if len(name) > max {
		name = name[:max]
	}
	return name
}

// publicBaseURL returns the configured PUBLIC_BASE_URL with any trailing
// slash trimmed. Used to build Stripe Checkout's success_url and cancel_url.
// main.go enforces that this is non-empty whenever Stripe is configured, so
// we never need a Host-header fallback here — a fallback would let an
// attacker hijack the success_url via a spoofed Host header.
func (h *Handlers) publicBaseURL() string {
	return strings.TrimRight(h.PublicBaseURL, "/")
}

// writeJSON is a small DRY for the response body shape. The callers all
// want application/json + a status + a JSON-encoded body.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

