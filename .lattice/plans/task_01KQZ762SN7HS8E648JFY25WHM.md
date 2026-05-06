# TYB-15: Require PUBLIC_BASE_URL when Stripe/Printful configured

### Problem

When `PUBLIC_BASE_URL` is unset, `httpserver.publicBaseURL()` (checkout_handlers.go:409-421) and `httpserver.publicFileURL()` (printful_handlers.go:447-463) fall back to a scheme + `r.Host` + `X-Forwarded-Proto`. That input is attacker-controlled outside a browser (curl). Two abuses:

1. **Stripe Checkout open-redirect / phishing.** A `Host: evil.example` request to `/api/checkout/start` produces a Session whose `success_url` is `https://evil.example/?session_id=...`. The attacker pays the $30 themselves, captures the URL, and tricks a victim into completing it.
2. **Persistent print-file substitution.** A request to `/api/printful/products` with `Host: evil.example` causes `files[].url` on the Printful sync_product to point at `https://evil.example/api/files/<hash>.png`. Because Printful's sync product is GET-first-by-`external_id` and the external_id is a 12-hex prefix of the design hash, the poisoned record is durable: when a real customer pays for that exact design, fulfillment fetches the print file from the attacker.

The fix: drop the `r.Host` fallback in both helpers and require `PUBLIC_BASE_URL` at boot whenever Stripe or Printful is configured. Failing closed at boot is preferable to a silent fallback that's only safe when the deployment topology happens to forward `Host` correctly.

### Approach: boot-time guard + helper rewrite + DI

#### 1. Boot-time enforcement (server/cmd/server/main.go)

Read `PUBLIC_BASE_URL` once near the top of `main()`, before `buildPrintful` and `buildStripe`. Add a new helper `validatePublicBaseURL(publicBaseURL string, stripeKey, printfulToken string) error` (or inline checks) that runs after token reads but before client construction:

- If `PUBLIC_BASE_URL == ""` AND (`STRIPE_SECRET_KEY != ""` OR `PRINTFUL_TOKEN != ""`): `logger.Fatalf("PUBLIC_BASE_URL is required when STRIPE_SECRET_KEY or PRINTFUL_TOKEN is set; refusing to boot to prevent Host-header spoofing of Stripe success_url and Printful print-file URLs")`. This message names the security reason so a sysadmin reading the log learns *why* the boot failed, not just that it did.
- If `PUBLIC_BASE_URL == ""` AND neither upstream is configured: log a single line `"PUBLIC_BASE_URL unset (Stripe and Printful both unconfigured; render path only)"` and proceed. The render-only flow doesn't construct any URL that's persisted upstream or used as a redirect.

The lookup of the env vars stays in `main.go`. The handlers receive the value via injection (see §3).

Refactor `buildPrintful` and `buildStripe` to take `publicBaseURL string` as an explicit parameter rather than reading the env directly. Drop the `WARNING` block in `buildPrintful` that previously logged about the missing var — that path is now fatal-handled upstream.

Optional but desirable hardening (flag for human review): require `https://` prefix when `STRIPE_MODE=live`. Stripe rejects non-HTTPS in live mode anyway, but boot-time rejection is friendlier than a runtime upstream 400. This is a small add; if controversial, defer.

#### 2. Helper rewrite (httpserver package)

New shape — both helpers become trivial:

```go
// publicFileURL builds the absolute URL Printful will GET to fetch the print
// PNG. The base is configured at boot (PUBLIC_BASE_URL); main.go fails fast
// when that env var is empty while Printful or Stripe is configured, so we
// don't accept the empty case here.
func (h *Handlers) publicFileURL(hash string) string {
    return strings.TrimRight(h.PublicBaseURL, "/") + "/api/files/" + hash + ".png"
}

func (h *Handlers) publicBaseURL() string {
    return strings.TrimRight(h.PublicBaseURL, "/")
}
```

Notes:
- Drop the `*http.Request` parameter — there's no per-request input anymore. Update both call sites (checkout_handlers.go:162, 196; printful_handlers.go:181, 385).
- `strings.TrimRight(..., "/")` handles trailing-slash normalization consistently for both `https://x` and `https://x/`.
- The helpers can stay as methods on `*Handlers` for symmetry; alternatively become package-level pure functions taking `(base, hash)`. Method-on-Handlers is the smaller diff. (Implementation note: keep them methods.)

#### 3. Dependency injection (httpserver.Handlers)

Move `PublicBaseURL` from `PrintfulSetup.PublicBaseURL` (printful_handlers.go:108) up to the top-level `Handlers` struct (handlers.go:47-53). Reasoning: the value isn't Printful-specific — Stripe's `publicBaseURL()` already reaches into `h.Printful.PublicBaseURL`, which is a code smell that this fix should clean up.

Diff sketch:

```go
type Handlers struct {
    Renderer      *render.Renderer
    Store         *files.Store
    Logger        *log.Logger
    PublicBaseURL string  // configured PUBLIC_BASE_URL; required when Printful or Stripe is non-nil
    Printful      *PrintfulSetup
    Stripe        *StripeSetup
}
```

Drop `PublicBaseURL` from `PrintfulSetup`. Update construction in `main.go` to set the field on `Handlers` (one line change). Update tests that wire `Handlers` directly to set the field.

#### 4. Edge cases & decisions to flag

- **Local dev without upstreams.** Confirmed: a developer running `go run ./cmd/server` with no `PRINTFUL_TOKEN` and no `STRIPE_SECRET_KEY` is unaffected by this change. The boot guard only fires when one of the two integrations is configured. The render-only routes (`/api/render`, `/api/files/*`) never call `publicFileURL`/`publicBaseURL`. The Printful and Stripe handlers all 503 before reaching the helpers when their dependencies are nil. Verified by code inspection of checkout_handlers.go:123-138, printful_handlers.go:168-179, printful_handlers.go:290-298, printful_handlers.go:373-383.
- **Trailing slash normalization.** Both shapes (`https://x.com` and `https://x.com/`) produce identical helper output via `strings.TrimRight(..., "/")`. Add a unit test covering both.
- **HTTPS enforcement.** Out of scope for V1; mention in a TODO comment near the boot guard. Live Stripe webhook delivery needs HTTPS anyway, so it's defense-in-depth, not a primary fix. Flagging for human review.
- **Static handler / Host header allowlist.** **Out of scope.** The `r.Host` value is fine for serving static assets (the `http.FileServer` fall-through). The vulnerability is only about URLs that are persisted upstream (Printful) or used as redirect targets (Stripe success/cancel). This task removes `r.Host` from those two helpers only; broader Host-header hardening is a separate concern.
- **The renderer's font path.** Untouched. Out of scope.
- **Existing `WARNING` log in `buildPrintful`** (main.go:153-158) describing the Host-header fallback gets removed — that fallback no longer exists.

#### 5. Tests

New (cmd/server):
- A simple unit test wouldn't cover `main()` directly. Either (a) extract `validatePublicBaseURL(publicBaseURL, stripeKey, printfulToken string) error` as a package-private helper in `main.go` with three test cases — no upstreams + empty base = nil error; Stripe configured + empty base = error; Printful configured + empty base = error; both configured + non-empty base = nil error — and call it from `main()`, or (b) skip the test and rely on integration. Recommendation: (a). Easier to verify the matrix.

Updated (httpserver):
- `printful_handlers_test.go:482-509` — `TestPublicFileURLPrefersConfigured` becomes simpler (no need to construct a request). `TestPublicFileURLFallsBackToHost` is **deleted** — that fallback is gone.
- Add a new test pair for trailing slash: helper returns `"https://x.com/api/files/abcd.png"` for both `"https://x.com"` and `"https://x.com/"` configured bases.
- Update `newTestHandlers` (printful_handlers_test.go:91-117) and `newCheckoutHandlers` (checkout_handlers_test.go:101-136) to set `h.PublicBaseURL` instead of `h.Printful.PublicBaseURL`.
- `stripe_webhook_test.go:100` similarly.
- All existing handler tests should keep passing once the field moves.

#### 6. Doc updates

- `.env.example` (lines 11-15): replace the comment with "Required when STRIPE_SECRET_KEY or PRINTFUL_TOKEN is set. Absolute URL Printful uses to GET the print PNG and Stripe uses to build success_url/cancel_url. Server fails to boot when this is empty and either upstream is configured."
- `README.md` (line 35): replace "When empty, the server falls back to the inbound `Host` header — fine for ngrok/cloudflared but not for plain `localhost`" with "Required when `PRINTFUL_TOKEN` or `STRIPE_SECRET_KEY` is configured; the server refuses to boot otherwise. This is enforced because the inbound `Host` header is attacker-controllable in non-browser clients (curl), and an unsafe fallback would let an attacker pin Printful sync_products at attacker hosts or hijack Stripe `success_url`."
- `README.md` line 88: leave the "Local with tunnel" wording — it already shows the correct setup.
- `server/cmd/server/main.go` (lines 15-19): tighten the package comment for `PUBLIC_BASE_URL` to match the new behavior — "Required when STRIPE_SECRET_KEY or PRINTFUL_TOKEN is set; server refuses to boot otherwise."

#### 7. Acceptance criteria

- Setting `STRIPE_SECRET_KEY` (any non-empty value) without `PUBLIC_BASE_URL` causes `go run ./cmd/server` to log a clear message naming both env vars and exit non-zero before binding the listener.
- Same for `PRINTFUL_TOKEN`.
- Same for both set together.
- With `PUBLIC_BASE_URL=https://example.com` set, `publicBaseURL()` returns `"https://example.com"` and `publicFileURL("abc")` returns `"https://example.com/api/files/abc.png"`. The inbound `Host` header on the request is unused.
- `PUBLIC_BASE_URL=https://example.com/` (trailing slash) produces identical output to without.
- With neither `STRIPE_SECRET_KEY` nor `PRINTFUL_TOKEN` set, the server boots successfully even when `PUBLIC_BASE_URL` is empty (render-only mode).
- Existing test suite (`go test ./...`) passes after updates.
- New unit test for `validatePublicBaseURL` covers the four-case matrix.
- A new helper unit test asserts the Host header is *not* read (e.g. by passing a request with `Host: evil.example` set and asserting the result still uses the configured base).

#### 8. Out of scope

- HTTPS-prefix validation (defense-in-depth; flag for human follow-up).
- Static handler / FileServer Host-header allowlist (separate concern).
- Renderer font path or any non-URL-construction code path.
- Generalized "validate all env vars at boot" framework (keep this task tight).
- Webhook signing or Stripe-mode mismatch logic (already implemented and unchanged).
