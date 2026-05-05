// Package stripe is a thin wrapper around the github.com/stripe/stripe-go/v82
// SDK, exposing only the two operations this server needs: CreateCheckoutSession
// and VerifyWebhook. The wrapper keeps the SDK's surface area out of the rest
// of the codebase so that an SDK upgrade or a Stripe API change is contained
// here.
package stripe

import "errors"

// ErrMissingKey is returned by New when Config.SecretKey is empty. main.go
// uses errors.Is(err, ErrMissingKey) to distinguish "configure me later" from
// "actually broken." Mirrors printful.ErrMissingToken.
var ErrMissingKey = errors.New("stripe: missing secret key")

// ErrInvalidSignature is returned by VerifyWebhook when the signature header
// is missing, malformed, or fails HMAC verification. The webhook handler
// translates this to a 400 response.
var ErrInvalidSignature = errors.New("stripe: invalid webhook signature")

// ErrModeMismatch is returned by New when the configured Mode (test/live) does
// not match the prefix of the supplied SecretKey. This is a fail-fast guard
// against accidentally pasting a live secret key into a dev .env file (or
// vice versa). main.go logs the error and proceeds with a nil client, so
// the create-checkout-start route degrades to 503 rather than the server
// taking real-money payments through a misconfigured environment.
var ErrModeMismatch = errors.New("stripe: STRIPE_MODE does not match secret key prefix")
