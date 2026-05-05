package printful

import (
	"context"
	"errors"
	"strings"
)

// CreateOrderRequest is the body for POST /store/orders. We carry the
// minimum viable set: external_id (idempotency key, set to the Stripe
// session id), recipient (mapped from Stripe's customer_details +
// shipping_details), and one item per sync_variant.
//
// retail_costs is optional — Printful computes shipping itself when omitted,
// which is what we want (free worldwide shipping is baked into the unit
// amount we charge on Stripe; we don't pass shipping cost through to
// Printful's invoicing).
type CreateOrderRequest struct {
	ExternalID string             `json:"external_id"`
	Recipient  OrderRecipient     `json:"recipient"`
	Items      []OrderItem        `json:"items"`
	RetailCosts *OrderRetailCosts `json:"retail_costs,omitempty"`
}

// OrderRecipient mirrors the Printful order.recipient struct. Field names
// match the v1 API spec; consult the table in the plan for the
// Stripe -> Printful mapping.
type OrderRecipient struct {
	Name        string `json:"name"`
	Address1    string `json:"address1"`
	Address2    string `json:"address2,omitempty"`
	City        string `json:"city"`
	StateCode   string `json:"state_code,omitempty"`
	CountryCode string `json:"country_code"`
	Zip         string `json:"zip"`
	Phone       string `json:"phone,omitempty"`
	Email       string `json:"email,omitempty"`
}

// OrderItem references an existing sync_variant by id (preferred) or by
// external_id. The webhook handler resolves sync_variant_id from the parent
// sync_product before placing the order.
type OrderItem struct {
	SyncVariantID int    `json:"sync_variant_id,omitempty"`
	ExternalID    string `json:"external_id,omitempty"`
	Quantity      int    `json:"quantity"`
	// RetailPrice is the price the customer paid for this line, in the same
	// currency as the order. Printful uses it for customs declarations on
	// international shipments.
	RetailPrice string `json:"retail_price,omitempty"`
}

// OrderRetailCosts is the customer-paid totals block. We populate it only
// to declare the line subtotal and the (zero) shipping the customer paid;
// Printful uses it for customs paperwork.
type OrderRetailCosts struct {
	Currency string `json:"currency,omitempty"`
	Subtotal string `json:"subtotal,omitempty"`
	Shipping string `json:"shipping,omitempty"`
	Tax      string `json:"tax,omitempty"`
	Total    string `json:"total,omitempty"`
}

// CreateOrderResponse mirrors the v1 envelope around an order. We expose the
// inner OrderData; callers read .ID and .Status (and treat 409s as success
// via ErrDuplicateExternalID).
type CreateOrderResponse struct {
	Code   int       `json:"code"`
	Result OrderData `json:"result"`
}

// OrderData is the live order — assigned id, status (typically "draft" or
// "pending"), and the external_id we passed in.
type OrderData struct {
	ID         int64  `json:"id"`
	ExternalID string `json:"external_id"`
	Status     string `json:"status"`
}

// ErrDuplicateExternalID is returned when Printful rejects a CreateOrder
// because an order with the same external_id already exists. The webhook
// handler treats this as success: idempotency at the Printful layer is the
// durable backstop for Stripe-retry storms across process restarts.
var ErrDuplicateExternalID = errors.New("printful: duplicate external_id")

// CreateOrder posts /store/orders?confirm=true so the order moves straight
// from draft to fulfillment. Auto-confirm is the V1 default (user-decided,
// see the plan): the moment Stripe confirms payment, Printful starts
// fulfillment. The signature-verified webhook is the gate keeping forged
// events from triggering real shipments.
//
// Returns ErrDuplicateExternalID when Printful rejects the create with a
// "external id is taken" / 409-style error. Callers (the webhook handler)
// treat that as a no-op success: the order was already placed on a previous
// delivery of the same Stripe event.
func (c *Client) CreateOrder(ctx context.Context, req CreateOrderRequest) (OrderData, error) {
	var resp CreateOrderResponse
	err := c.do(ctx, "POST", "/store/orders?confirm=true", req, &resp)
	if err != nil {
		// Printful's behaviour on duplicate external_id is documented as a
		// 409 with a "Provided external id is already used" message. We
		// match on either the status or the message so a future Printful
		// status-code shift doesn't silently break idempotency.
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == 409 {
				return OrderData{}, ErrDuplicateExternalID
			}
			if apiErr.StatusCode == 400 && strings.Contains(strings.ToLower(apiErr.Message), "external") &&
				strings.Contains(strings.ToLower(apiErr.Message), "already") {
				return OrderData{}, ErrDuplicateExternalID
			}
		}
		return OrderData{}, err
	}
	return resp.Result, nil
}
