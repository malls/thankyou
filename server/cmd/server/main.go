// Command server starts the Thank You HTTP server.
//
// Static site assets, the SVG render template, and the woff font are all
// baked into the binary at compile time, so the resulting binary is
// self-contained: copy it onto a host, run it, done.
//
// Environment variables:
//
//	PORT                  — listen port; defaults to 8080.
//	DATA_DIR              — directory for saved PNGs; defaults to ./data/files.
//	RENDERER_POOL_SIZE    — number of parallel render slots; defaults to 4.
//	                        Each slot owns its own wasm runtime + font db
//	                        (~10–30MB resident). Raise this if /api/render,
//	                        /api/printful/products, or /api/checkout/start
//	                        start queueing under load. Invalid or non-positive
//	                        values are clamped to 1 with a warning log.
//	PRINTFUL_TOKEN        — Printful bearer token. When unset, the
//	                        /api/printful/* and /api/checkout/* routes
//	                        return 503 with a typed error code.
//	PRINTFUL_STORE_ID     — optional X-PF-Store-Id header (account-level tokens).
//	PUBLIC_BASE_URL       — absolute URL Printful uses to GET the print PNG
//	                        and Stripe uses for success_url/cancel_url
//	                        (e.g. https://abc.ngrok.app). Required when
//	                        STRIPE_SECRET_KEY or PRINTFUL_TOKEN is set;
//	                        server refuses to boot otherwise.
//	STRIPE_SECRET_KEY     — Stripe secret/restricted API key. Unset = 503 on
//	                        /api/checkout/start with stripe_unconfigured.
//	STRIPE_WEBHOOK_SECRET — signing secret from `stripe listen` (test) or
//	                        the dashboard (live). Required for the webhook.
//	STRIPE_MODE           — "test" or "live"; asserted at startup against
//	                        STRIPE_SECRET_KEY's prefix. Mismatch fails
//	                        loudly and the create-checkout-start route 503s.
//	STRIPE_PRICE_USD_CENTS — optional override for the per-tee unit price in
//	                         USD cents. Empty falls back to the catalog
//	                         (DefaultRetailPrice × 100; currently 3000).
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/forrestalmasi/thankyou/server/internal/files"
	"github.com/forrestalmasi/thankyou/server/internal/httpserver"
	"github.com/forrestalmasi/thankyou/server/internal/printful"
	"github.com/forrestalmasi/thankyou/server/internal/render"
	tystripe "github.com/forrestalmasi/thankyou/server/internal/stripe"
)

func main() {
	logger := log.New(os.Stdout, "thankyou: ", log.LstdFlags|log.Lmicroseconds)

	port := envOr("PORT", "8080")
	dataDir := envOr("DATA_DIR", "./data/files")
	poolSize := rendererPoolSize(logger)

	// Read PUBLIC_BASE_URL alongside the upstream credentials so we can fail
	// fast at boot when the combination is unsafe. The Host-header fallback
	// that used to live in the helpers is gone — without an explicit base,
	// an attacker controlling Host: could pin Printful sync_products at an
	// attacker host or hijack Stripe's success_url.
	publicBaseURL := os.Getenv("PUBLIC_BASE_URL")
	stripeKey := os.Getenv("STRIPE_SECRET_KEY")
	printfulToken := os.Getenv("PRINTFUL_TOKEN")
	if err := validatePublicBaseURL(publicBaseURL, stripeKey, printfulToken); err != nil {
		logger.Fatalf("%v", err)
	}
	if publicBaseURL == "" {
		// Render-only mode: neither Stripe nor Printful is configured, so
		// no URL we hand to a third party gets constructed. Safe to proceed.
		logger.Printf("PUBLIC_BASE_URL unset (Stripe and Printful both unconfigured; render path only)")
	}
	// TODO: reject non-https PUBLIC_BASE_URL when STRIPE_MODE=live; deferred
	// to a follow-up task.

	store, err := files.New(dataDir)
	if err != nil {
		logger.Fatalf("init file store: %v", err)
	}
	logger.Printf("file store rooted at %s", store.Dir())

	// Boot the renderer up front so font-load failures are fatal at startup
	// rather than per-request 500s. The wasm runtime takes ~50ms per slot.
	renderer, err := render.NewRenderer(context.Background(), poolSize)
	if err != nil {
		logger.Fatalf("init renderer: %v", err)
	}
	logger.Printf("renderer pool size: %d", poolSize)
	if poolSize == 1 {
		logger.Printf("renderer pool size: 1 (no parallelism; raise RENDERER_POOL_SIZE for throughput)")
	}
	defer func() {
		if err := renderer.Close(); err != nil {
			logger.Printf("renderer close: %v", err)
		}
	}()

	// Printful is optional. Missing token → handlers see nil and 503 the
	// /api/printful/* routes with file_id+file_url so the UI still works.
	// We don't fatal-fail the boot for it; the render path is independently
	// useful in dev.
	pfSetup := buildPrintful(logger, printfulToken)
	stripeSetup := buildStripe(logger, stripeKey)

	handler, err := httpserver.NewRouter(&httpserver.Handlers{
		Renderer:      renderer,
		Store:         store,
		Logger:        logger,
		PublicBaseURL: publicBaseURL,
		Printful:      pfSetup,
		Stripe:        stripeSetup,
	})
	if err != nil {
		logger.Fatalf("init router: %v", err)
	}

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// Renders take a couple seconds; allow generous totals.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Listen-and-serve in its own goroutine so we can intercept signals
	// and shut down cleanly. Without this, a Ctrl-C would kill in-flight
	// renders mid-write and leave .tmp files lying around.
	errCh := make(chan error, 1)
	go func() {
		logger.Printf("listening on http://localhost:%s", port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Printf("received %s, shutting down", sig)
	case err := <-errCh:
		logger.Printf("server error: %v", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown: %v", err)
	}
}

// envOr returns the named env var or `fallback` when the var is unset/empty.
// One-liner wrapper purely for readability at the call site.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// rendererPoolSize parses RENDERER_POOL_SIZE with a default of 4. Invalid or
// non-positive values clamp to 1 with a warning log — a typo shouldn't take
// the server down. The default is memory-bound (each slot carries its own
// wasm runtime + font db, ~10–30MB resident), so 4 is a sensible fixed
// default; operators raise it explicitly when render throughput matters.
func rendererPoolSize(logger *log.Logger) int {
	const defaultSize = 4
	v := os.Getenv("RENDERER_POOL_SIZE")
	if v == "" {
		return defaultSize
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		logger.Printf("WARNING: RENDERER_POOL_SIZE=%q is not an integer; clamping to 1", v)
		return 1
	}
	if n <= 0 {
		logger.Printf("WARNING: RENDERER_POOL_SIZE=%d is not positive; clamping to 1", n)
		return 1
	}
	return n
}

// validatePublicBaseURL fails closed when PUBLIC_BASE_URL is empty while
// either Stripe or Printful is configured. The Host-header fallback used to
// silently absorb the empty case, but it's attacker-controllable in non-
// browser clients (curl Host: evil.example) — a missing base would let an
// attacker pin Printful's sync_product file URL at their host (durable: keyed
// on external_id from the design hash) or hijack Stripe's success_url. With
// neither upstream configured the renderer-only path is safe, so we return
// nil and let main proceed.
func validatePublicBaseURL(publicBaseURL, stripeKey, printfulToken string) error {
	if publicBaseURL != "" {
		return nil
	}
	if stripeKey == "" && printfulToken == "" {
		return nil
	}
	return errors.New("PUBLIC_BASE_URL is required when STRIPE_SECRET_KEY or PRINTFUL_TOKEN is set; " +
		"refusing to boot to prevent Host-header spoofing of Stripe success_url and Printful print-file URLs")
}

// buildPrintful constructs the optional Printful integration from env. Returns
// nil when PRINTFUL_TOKEN is unset; the handler layer detects nil and 503s
// the /api/printful/* routes with the saved file_id/file_url. The base URL
// is supplied by the caller (validated upstream in validatePublicBaseURL).
func buildPrintful(logger *log.Logger, token string) *httpserver.PrintfulSetup {
	if token == "" {
		logger.Printf("PRINTFUL_TOKEN unset; /api/printful/* will 503 (render path still works)")
		return nil
	}
	storeID := os.Getenv("PRINTFUL_STORE_ID")
	c, err := printful.New(printful.Config{
		Token:   token,
		StoreID: storeID,
		Logger:  logger,
	})
	if err != nil {
		// Should not happen — token is non-empty above. Log and skip.
		logger.Printf("printful.New: %v; /api/printful/* will 503", err)
		return nil
	}
	logger.Printf("printful integration enabled (store_id=%q)", storeID)
	return &httpserver.PrintfulSetup{
		Client: c,
	}
}

// buildStripe constructs the optional Stripe Checkout integration from env.
// Returns nil when STRIPE_SECRET_KEY is unset; the handler layer detects nil
// and 503s the /api/checkout/start and /api/stripe/webhook routes with
// {"error":"stripe_unconfigured"}.
//
// STRIPE_MODE is asserted against the secret-key prefix at construction time
// (sk_test_/rk_test_ vs sk_live_/rk_live_). A mismatch fails loudly and we
// pass nil so the create-session route degrades to 503 — failing closed is
// the right behaviour, especially for the live-key-in-dev case where the
// alternative is taking real-money payments through a misconfigured server.
func buildStripe(logger *log.Logger, key string) *httpserver.StripeSetup {
	if key == "" {
		logger.Printf("STRIPE_SECRET_KEY unset; /api/checkout/start and /api/stripe/webhook will 503")
		return nil
	}
	mode := tystripe.Mode(os.Getenv("STRIPE_MODE"))
	if mode == "" {
		logger.Printf("WARNING: STRIPE_SECRET_KEY set but STRIPE_MODE empty; refusing to construct Stripe client (set STRIPE_MODE=test or STRIPE_MODE=live)")
		return nil
	}
	webhookSecret := os.Getenv("STRIPE_WEBHOOK_SECRET")
	if webhookSecret == "" {
		logger.Printf("WARNING: STRIPE_SECRET_KEY set but STRIPE_WEBHOOK_SECRET empty; the /api/stripe/webhook route will reject every request until you copy the whsec_ from `stripe listen` into .env")
	}

	c, err := tystripe.New(tystripe.Config{
		SecretKey:     key,
		WebhookSecret: webhookSecret,
		Mode:          mode,
		Logger:        logger,
	})
	if err != nil {
		// Mode mismatch (or missing-key) — log and pass nil so the route
		// 503s. Better to fail closed than to take a payment through a
		// misconfigured environment.
		logger.Printf("stripe.New failed: %v; /api/checkout/start and /api/stripe/webhook will 503", err)
		return nil
	}

	var override int64
	if v := os.Getenv("STRIPE_PRICE_USD_CENTS"); v != "" {
		parsed, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || parsed <= 0 {
			logger.Printf("WARNING: STRIPE_PRICE_USD_CENTS=%q is not a positive integer; ignoring (using catalog default)", v)
		} else {
			override = parsed
			logger.Printf("stripe: STRIPE_PRICE_USD_CENTS override set to %d cents", override)
		}
	}
	return &httpserver.StripeSetup{
		Client:             c,
		PriceCentsOverride: override,
	}
}
