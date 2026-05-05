package httpserver

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	stripego "github.com/stripe/stripe-go/v82"

	"github.com/forrestalmasi/thankyou/server/internal/printful"
	tystripe "github.com/forrestalmasi/thankyou/server/internal/stripe"
)

// MaxWebhookBodyBytes caps the inbound webhook body. Stripe payloads are
// well under 30 KiB; the 1 MiB cap is defence against a hostile proxy.
const MaxWebhookBodyBytes = 1 << 20

// stripeCheckoutSessionView is the subset of the Stripe checkout.session
// object the webhook handler needs. We unmarshal the event's Data.Raw into
// this rather than the full SDK CheckoutSession struct — the SDK type carries
// 100+ fields, most via custom UnmarshalJSON, and we only need a handful.
//
// Field tags must match Stripe's wire format exactly. Mapping table:
//   - id                          → SessionID for idempotency + external_id
//   - payment_status              → paid/unpaid/no_payment_required gate
//   - metadata                    → server-controlled keys (sync_product_id, …)
//   - customer_details            → name/email/phone for Printful recipient
//   - shipping_details            → address for Printful recipient
type stripeCheckoutSessionView struct {
	ID              string                  `json:"id"`
	Object          string                  `json:"object"`
	PaymentStatus   string                  `json:"payment_status"`
	Metadata        map[string]string       `json:"metadata"`
	CustomerDetails *stripeCustomerDetails  `json:"customer_details"`
	ShippingDetails *stripeShippingDetails  `json:"shipping_details"`
}

type stripeCustomerDetails struct {
	Email   string         `json:"email"`
	Name    string         `json:"name"`
	Phone   string         `json:"phone"`
	Address *stripeAddress `json:"address"`
}

type stripeShippingDetails struct {
	Name    string         `json:"name"`
	Address *stripeAddress `json:"address"`
}

type stripeAddress struct {
	City       string `json:"city"`
	Country    string `json:"country"`
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	PostalCode string `json:"postal_code"`
	State      string `json:"state"`
}

// StripeWebhook handles POST /api/stripe/webhook. Steps documented in the
// plan §"Webhook design":
//
//  1. Cap body at 1 MiB and read the raw bytes (signature is over the body).
//  2. Verify the Stripe-Signature header against STRIPE_WEBHOOK_SECRET.
//     400 on any failure — Stripe will stop retrying after a few attempts.
//  3. Switch on event type. Only checkout.session.completed is handled in
//     V1; everything else 200s (silently ack so Stripe stops retrying).
//  4. Idempotency: in-memory sync.Map keyed by session.id stops duplicate
//     processing within the same process; Printful's external_id check is
//     the durable layer that survives restarts.
//  5. Pull metadata, build the Printful order request, POST it.
//  6. Map Printful's response back to a status code:
//     - 2xx or duplicate external_id → 200 (success / no-op)
//     - 5xx → 5xx response so Stripe retries
//     - other 4xx → 200 + alert log (we can't recover automatically)
func (h *Handlers) StripeWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "", "POST required")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, MaxWebhookBodyBytes)
	defer r.Body.Close()

	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "body_read_failed", "", err.Error())
		return
	}

	if h.Stripe == nil || h.Stripe.Client == nil {
		// Configured-nil → reject. Loud rather than silent so a misconfigured
		// deploy fails closed (no signature → no idempotency → no order).
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error":   "stripe_unconfigured",
			"message": "server is missing STRIPE_SECRET_KEY",
		})
		return
	}

	sigHeader := r.Header.Get("Stripe-Signature")
	event, err := h.Stripe.Client.VerifyWebhook(body, sigHeader)
	if err != nil {
		// Don't echo the upstream error message — could leak diagnostic
		// detail. The 400 alone is enough; details go to the log.
		h.logf("stripe/webhook: signature verify failed: %v", err)
		_ = errors.Is(err, tystripe.ErrInvalidSignature) // explicit: callers can errors.Is on the wrapped err if useful
		writeError(w, http.StatusBadRequest, "invalid_signature", "", "signature verification failed")
		return
	}

	if event.Type != "checkout.session.completed" {
		// Other event types: silently ack. Stripe will retry non-2xx; we
		// don't want to retry-loop on a misconfigured webhook subscription.
		h.logf("stripe/webhook: ignoring event type=%s id=%s", event.Type, event.ID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "ignored": event.Type})
		return
	}

	var session stripeCheckoutSessionView
	if event.Data == nil || len(event.Data.Raw) == 0 {
		h.logf("stripe/webhook: event %s has no data — acking and skipping", event.ID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "empty_data"})
		return
	}
	if err := json.Unmarshal(event.Data.Raw, &session); err != nil {
		h.logf("stripe/webhook: unmarshal session failed event_id=%s err=%v", event.ID, err)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "bad_session_payload"})
		return
	}

	// In-process idempotency. LoadOrStore returns true for the second caller
	// that races onto the same session id; we 200 it without re-doing work.
	if _, dup := h.Stripe.SeenSessions.LoadOrStore(session.ID, struct{}{}); dup {
		h.logf("stripe/webhook: duplicate session %s — already processed", session.ID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "duplicate": true})
		return
	}

	// Delayed-payment methods (ACH, etc.) fire this event with payment_status
	// in {"unpaid","no_payment_required"}. We don't enable those in V1; if a
	// customer somehow uses one, log loudly and skip. async_payment_succeeded
	// would be the right event to subscribe to in that case.
	if session.PaymentStatus != "paid" {
		h.logf("stripe/webhook: session %s payment_status=%q — skipping",
			session.ID, session.PaymentStatus)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "not_paid"})
		return
	}

	syncProductID, variantID, ok := pullOrderMetadata(session.Metadata)
	if !ok {
		// Loud log + 200. We can't recover automatically; the operator
		// must refund manually via the Stripe dashboard.
		h.logf("stripe/webhook: ALERT session %s missing/invalid metadata: %+v",
			session.ID, session.Metadata)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "missing_metadata"})
		return
	}

	// Resolve sync_variant_id: Printful order items reference the variant by
	// the sync_variants[].id from the parent product. Re-fetch the product
	// rather than relying on a server-side cache (no extra storage tier).
	product, err := h.Printful.Client.GetSyncProductByExternalID(r.Context(), session.Metadata["external_id"])
	if err != nil {
		// 5xx if the upstream is broken so Stripe retries. Other classes
		// (404 product gone, 401 token rotated) get a 200 + alert log.
		h.handleWebhookPrintfulErr(w, session.ID, "fetch_sync_product", err)
		return
	}
	syncVariantID, syncVariantExternalID := lookupSyncVariant(product, variantID)
	if syncVariantID == 0 && syncVariantExternalID == "" {
		h.logf("stripe/webhook: ALERT session %s product=%d has no sync_variant for variant_id=%d",
			session.ID, syncProductID, variantID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "missing_sync_variant"})
		return
	}

	recipient := mapStripeRecipient(&session)
	if recipient.Address1 == "" || recipient.CountryCode == "" {
		h.logf("stripe/webhook: ALERT session %s missing shipping address",
			session.ID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "skipped": "missing_address"})
		return
	}

	orderItem := printful.OrderItem{
		Quantity:    1,
		RetailPrice: retailPriceForVariant(variantID),
	}
	if syncVariantID > 0 {
		orderItem.SyncVariantID = syncVariantID
	} else {
		orderItem.ExternalID = syncVariantExternalID
	}
	orderReq := printful.CreateOrderRequest{
		ExternalID: session.ID,
		Recipient:  recipient,
		Items:      []printful.OrderItem{orderItem},
	}
	order, err := h.Printful.Client.CreateOrder(r.Context(), orderReq)
	if errors.Is(err, printful.ErrDuplicateExternalID) {
		h.logf("stripe/webhook: session %s already has a Printful order — treating as success", session.ID)
		writeJSON(w, http.StatusOK, map[string]any{"received": true, "duplicate_order": true})
		return
	}
	if err != nil {
		h.handleWebhookPrintfulErr(w, session.ID, "create_order", err)
		return
	}

	h.logf("stripe/webhook: session=%s printful_order_id=%d status=%s ok",
		session.ID, order.ID, order.Status)
	writeJSON(w, http.StatusOK, map[string]any{
		"received":          true,
		"printful_order_id": order.ID,
		"status":            order.Status,
	})
}

// handleWebhookPrintfulErr maps a Printful client error to the right webhook
// response. 5xx → 5xx (Stripe retries); 4xx other → 200 + alert log. We
// also un-stamp the SeenSessions entry on retryable failure so a subsequent
// retry from Stripe can proceed.
func (h *Handlers) handleWebhookPrintfulErr(w http.ResponseWriter, sessionID, stage string, err error) {
	var apiErr *printful.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode >= 500 {
		h.logf("stripe/webhook: session=%s stage=%s upstream 5xx err=%v — returning 5xx for Stripe retry",
			sessionID, stage, err)
		// Allow Stripe's retry to redeliver: clear our in-memory dedup
		// stamp so the retry isn't dropped by the duplicate check.
		h.Stripe.SeenSessions.Delete(sessionID)
		writeError(w, http.StatusBadGateway, "printful_upstream_error", "", "")
		return
	}
	// Other 4xx: log and ack. Manual operator intervention.
	h.logf("stripe/webhook: ALERT session=%s stage=%s non-retriable err=%v",
		sessionID, stage, err)
	writeJSON(w, http.StatusOK, map[string]any{
		"received": true,
		"skipped":  stage + "_failed",
	})
}

// pullOrderMetadata extracts the typed fields the webhook needs from the
// Session's metadata block. Returns ok=false on any missing or malformed
// value so the caller can log+ack rather than retry forever.
func pullOrderMetadata(meta map[string]string) (syncProductID int64, variantID int, ok bool) {
	if meta == nil {
		return 0, 0, false
	}
	syncStr := meta["sync_product_id"]
	variantStr := meta["variant_id"]
	if syncStr == "" || variantStr == "" {
		return 0, 0, false
	}
	syncProductID, err := strconv.ParseInt(syncStr, 10, 64)
	if err != nil || syncProductID <= 0 {
		return 0, 0, false
	}
	variantID, err = strconv.Atoi(variantStr)
	if err != nil || variantID <= 0 {
		return 0, 0, false
	}
	return syncProductID, variantID, true
}

// lookupSyncVariant finds the sync_variant whose .variant_id matches the
// catalog variant the customer bought. Returns the numeric sync_variant id
// (preferred for OrderItem.SyncVariantID) and the external_id (fallback).
// Both zero/empty when not found.
func lookupSyncVariant(product printful.SyncProductData, variantID int) (int, string) {
	for _, sv := range product.SyncVariants {
		if sv.VariantID == variantID {
			return int(sv.ID), sv.ExternalID
		}
	}
	return 0, ""
}

// retailPriceForVariant returns the RetailPrice string the catalog assigns
// to a variant. Used in the OrderItem to declare per-line customs price.
func retailPriceForVariant(variantID int) string {
	for _, dv := range printful.DefaultVariants {
		if dv.VariantID == variantID {
			return dv.RetailPrice
		}
	}
	return printful.DefaultRetailPrice
}

// mapStripeRecipient turns the Stripe session's customer/shipping fields
// into a Printful recipient. The mapping table is documented in the plan
// §"Server changes — orders".
func mapStripeRecipient(s *stripeCheckoutSessionView) printful.OrderRecipient {
	r := printful.OrderRecipient{}
	if s.CustomerDetails != nil {
		r.Name = s.CustomerDetails.Name
		r.Email = s.CustomerDetails.Email
		r.Phone = s.CustomerDetails.Phone
	}
	// Shipping name takes priority when distinct from billing.
	if s.ShippingDetails != nil {
		if s.ShippingDetails.Name != "" {
			r.Name = s.ShippingDetails.Name
		}
		if s.ShippingDetails.Address != nil {
			r.Address1 = s.ShippingDetails.Address.Line1
			r.Address2 = s.ShippingDetails.Address.Line2
			r.City = s.ShippingDetails.Address.City
			r.StateCode = s.ShippingDetails.Address.State
			r.CountryCode = s.ShippingDetails.Address.Country
			r.Zip = s.ShippingDetails.Address.PostalCode
		}
	}
	// Sanitise: Printful expects upper-case country codes.
	r.CountryCode = strings.ToUpper(r.CountryCode)
	return r
}

// _ = stripego.Event{} — silence unused-import detection in case a future
// edit drops the only stripego ref above. (No real overhead; Go will
// optimise the discard away.)
var _ = stripego.Event{}
