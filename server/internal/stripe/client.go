package stripe

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	stripego "github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"
)

// Mode is "test" or "live". Asserted at construction time against the secret
// key prefix so a misconfigured .env (live key with STRIPE_MODE=test, or
// vice versa) fails fast instead of taking real-money payments through a
// dev environment. See ErrModeMismatch.
type Mode string

const (
	// ModeTest is the dev/sandbox mode. Secret keys must start with sk_test_
	// or rk_test_ (restricted-key flavour).
	ModeTest Mode = "test"
	// ModeLive is the production mode. Secret keys must start with sk_live_
	// or rk_live_. New() also logs a loud "live mode active" line on boot
	// so an operator who started the server by accident notices.
	ModeLive Mode = "live"
)

// Client is the wrapped Stripe client. Construct via New, share across
// goroutines (the underlying *stripego.Client is safe for concurrent use).
type Client struct {
	sdk           *stripego.Client
	webhookSecret string
	mode          Mode
	logger        *log.Logger
}

// Config bundles the constructor arguments. SecretKey and Mode are required;
// everything else has a sensible default.
type Config struct {
	// SecretKey is the Stripe secret API key (sk_test_..., sk_live_...,
	// rk_test_..., rk_live_...). Required — New returns ErrMissingKey when
	// empty so main.go can decide whether to nil-pass the client (graceful
	// 503) or fatal-fail.
	SecretKey string

	// WebhookSecret is the signing secret printed by `stripe listen` (test)
	// or set on the dashboard's webhook endpoint (live). Used only by
	// VerifyWebhook; the create-session path does not need it. May be empty
	// at construction time (the webhook handler then rejects every request),
	// which is the right behaviour while a dev hasn't yet started
	// `stripe listen`.
	WebhookSecret string

	// Mode is "test" or "live". Asserted at construction time against the
	// secret key prefix; mismatches return ErrModeMismatch.
	Mode Mode

	// BaseURL overrides the upstream Stripe host for tests. Empty means use
	// the SDK default (https://api.stripe.com). Tests mount an httptest.Server
	// here.
	BaseURL string

	// HTTPClient lets the caller supply a pre-configured *http.Client. When
	// nil, the SDK constructs its own. Tests use this to inject a custom
	// Transport.
	HTTPClient *http.Client

	// Logger receives a single boot line summarising mode + base URL. Token
	// material is NEVER logged. Nil disables logging.
	Logger *log.Logger
}

// New constructs a Client. Returns ErrMissingKey when SecretKey is empty and
// ErrModeMismatch when Mode and the key's prefix disagree. Callers that want
// graceful degradation should detect the typed errors and pass nil to the
// HTTP layer (which then 503s the create-checkout-start route, mirroring the
// printful.ErrMissingToken pattern).
func New(cfg Config) (*Client, error) {
	if cfg.SecretKey == "" {
		return nil, ErrMissingKey
	}
	if cfg.Mode == "" {
		// Mode is required so that omitting it is a configuration error,
		// not a silent default-to-live or default-to-test. Surface it as
		// a mismatch so the operator gets a clear message.
		return nil, fmt.Errorf("%w: STRIPE_MODE is required", ErrModeMismatch)
	}
	if err := assertModeMatchesKey(cfg.Mode, cfg.SecretKey); err != nil {
		return nil, err
	}

	// The SDK reads its base URL from BackendConfig.URL, which is per-backend.
	// We override the API backend (the only one this server uses) so tests
	// can mount an httptest.Server.
	var opts []stripego.ClientOption
	if cfg.BaseURL != "" || cfg.HTTPClient != nil {
		bc := &stripego.BackendConfig{}
		if cfg.BaseURL != "" {
			url := cfg.BaseURL
			bc.URL = &url
		}
		if cfg.HTTPClient != nil {
			bc.HTTPClient = cfg.HTTPClient
		}
		// Network retries inside the SDK confuse "did Stripe see my request?"
		// telemetry in tests. Disable for the test backend; production
		// callers don't override BaseURL/HTTPClient so they still get the
		// SDK default of 2 retries.
		zero := int64(0)
		bc.MaxNetworkRetries = &zero
		backends := stripego.NewBackendsWithConfig(bc)
		opts = append(opts, stripego.WithBackends(backends))
	}

	sdk := stripego.NewClient(cfg.SecretKey, opts...)

	c := &Client{
		sdk:           sdk,
		webhookSecret: cfg.WebhookSecret,
		mode:          cfg.Mode,
		logger:        cfg.Logger,
	}
	if cfg.Logger != nil {
		base := cfg.BaseURL
		if base == "" {
			base = stripego.APIURL
		}
		cfg.Logger.Printf("stripe: client constructed mode=%s base_url=%s webhook_secret_set=%t",
			cfg.Mode, base, cfg.WebhookSecret != "")
		if cfg.Mode == ModeLive {
			cfg.Logger.Printf("stripe: LIVE MODE ACTIVE — real money will move")
		}
	}
	return c, nil
}

// assertModeMatchesKey returns ErrModeMismatch when mode and the key's prefix
// disagree. Accepts both standard (sk_) and restricted (rk_) keys.
func assertModeMatchesKey(mode Mode, key string) error {
	switch mode {
	case ModeTest:
		if strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "rk_test_") {
			return nil
		}
		return fmt.Errorf("%w: STRIPE_MODE=test but key prefix is not sk_test_/rk_test_", ErrModeMismatch)
	case ModeLive:
		if strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_live_") {
			return nil
		}
		return fmt.Errorf("%w: STRIPE_MODE=live but key prefix is not sk_live_/rk_live_", ErrModeMismatch)
	default:
		return fmt.Errorf("%w: unknown mode %q (expected test or live)", ErrModeMismatch, mode)
	}
}

// Mode returns the configured mode. Mostly useful for log lines and tests.
func (c *Client) Mode() Mode { return c.mode }

// CreateCheckoutSession wraps stripego.V1CheckoutSessions.Create. Returns
// the underlying SDK type so callers can read .ID, .URL, etc. directly —
// keeping the wrapper thin minimises the surface area to mock when the SDK
// changes.
func (c *Client) CreateCheckoutSession(ctx context.Context, params *stripego.CheckoutSessionCreateParams) (*stripego.CheckoutSession, error) {
	if c == nil || c.sdk == nil {
		return nil, ErrMissingKey
	}
	return c.sdk.V1CheckoutSessions.Create(ctx, params)
}

// VerifyWebhook validates the Stripe-Signature header against the configured
// WebhookSecret. Returns ErrInvalidSignature on any verification failure
// (missing header, malformed, expired tolerance window, HMAC mismatch). On
// success, returns the parsed Event whose Data.Raw the caller can unmarshal
// into the appropriate resource type.
//
// The 5-minute default tolerance comes from the SDK; we don't customise it.
// Body size capping is the caller's responsibility (handlers wrap with
// http.MaxBytesReader).
func (c *Client) VerifyWebhook(payload []byte, sigHeader string) (stripego.Event, error) {
	if c == nil {
		return stripego.Event{}, ErrInvalidSignature
	}
	if c.webhookSecret == "" {
		// No secret configured → reject every request. Loud rather than
		// silent so a misconfigured deploy fails closed.
		return stripego.Event{}, fmt.Errorf("%w: STRIPE_WEBHOOK_SECRET is unset", ErrInvalidSignature)
	}
	// We pass IgnoreAPIVersionMismatch=true: the SDK's hard-coded API version
	// can drift from what's actually configured in the dashboard, and we
	// don't depend on per-version field shapes for the small handful of
	// fields we read. Better to ack-and-process than to 400 a legitimately
	// signed event because of an SDK pin.
	ev, err := webhook.ConstructEventWithOptions(payload, sigHeader, c.webhookSecret, webhook.ConstructEventOptions{
		Tolerance:                webhook.DefaultTolerance,
		IgnoreAPIVersionMismatch: true,
	})
	if err != nil {
		return stripego.Event{}, fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}
	return ev, nil
}
