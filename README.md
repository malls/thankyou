# thankyou

GitHub Pages site for generating text in the classic "THANK YOU" plastic bag style. Now also has a Go server in [server/](server/) that renders print-quality PNGs from the same SVG template, paving the way for fulfillment.

Hosted at [thankyoubag.online](https://thankyoubag.online).

## Run the server locally

The server bakes the static site and the woff font into a single binary via `//go:embed`. No dependencies beyond Go (1.22+).

1. Clone the repo, then `cd server`.

2. Refresh embedded assets:

   ```
   ./tools/copy-static.sh
   ```

   See [server/tools/copy-static.sh](server/tools/copy-static.sh). It copies the repo-root static files into [server/internal/httpserver/static/](server/internal/httpserver/static/) so the embed directive in [server/internal/httpserver/static.go](server/internal/httpserver/static.go) picks them up at build time. Re-run this whenever you edit any of [index.html](index.html), [style.css](style.css), [script.js](script.js), [favicon.ico](favicon.ico), [splash.png](splash.png), [Helvetica-Black.woff](Helvetica-Black.woff), or [Helvetica-Black.woff2](Helvetica-Black.woff2).

3. Set up env vars and start the server:

   ```
   cp ../.env.example ../.env
   # edit ../.env, then:
   ./tools/run-dev.sh
   ```

   [server/tools/run-dev.sh](server/tools/run-dev.sh) sources `<repo>/.env` if present and `exec`s `go run ./cmd/server`. The wrapper is the recommended path; if you'd rather not use it:

   ```
   set -a; source ../.env; set +a; go run ./cmd/server
   ```

   Or just export the vars by hand. The Go binary itself reads only `os.Getenv` — it does not load `.env` on its own.

   Listens on `:8080` by default. The full key list lives in [.env.example](.env.example); descriptions:

   - `PORT` — listen port (default `8080`).
   - `DATA_DIR` — directory for saved PNGs (default `./data/files`).
   - `PRINTFUL_TOKEN` — Printful bearer token. **Required** for the `/api/printful/*` and `/api/checkout/*` routes; when unset they return 503 with a typed error so the rest of the UI still works.
   - `PRINTFUL_STORE_ID` — optional `X-PF-Store-Id` header for account-level tokens. Store-level tokens leave this empty.
   - `PUBLIC_BASE_URL` — absolute URL Printful uses to GET the print PNG (e.g. `https://abc.ngrok.app`). Also the prefix used to build Stripe Checkout's `success_url`/`cancel_url`. When empty, the server falls back to the inbound `Host` header — fine for ngrok/cloudflared but not for plain `localhost`.
   - `STRIPE_SECRET_KEY` — Stripe secret/restricted API key. Unset = `/api/checkout/start` 503s with `stripe_unconfigured`.
   - `STRIPE_WEBHOOK_SECRET` — signing secret from `stripe listen` (test) or the dashboard (live). Required for `/api/stripe/webhook`; when empty the webhook rejects every request.
   - `STRIPE_MODE` — `test` or `live`. Asserted at startup against `STRIPE_SECRET_KEY`'s prefix (`sk_test_*`/`rk_test_*` vs `sk_live_*`/`rk_live_*`). Mismatch fails fast and the create-checkout-start route degrades to 503.
   - `STRIPE_PRICE_USD_CENTS` — optional override for the per-tee unit price in USD cents. Falls back to the variant's `RetailPrice` from [server/internal/printful/catalog.go](server/internal/printful/catalog.go) × 100 (currently $30 → `3000`).

   See [server/cmd/server/main.go](server/cmd/server/main.go).

4. Verify:

   - `curl http://localhost:8080/healthz` returns `ok`.
   - `http://localhost:8080/` serves the embedded static site.
   - `curl -X POST http://localhost:8080/api/render -d '{"text":"FOO","middletext":"BAR"}'` returns `{"file_id","url"}` and writes `data/files/{hash}.png`. Fetch the PNG at `GET /api/files/{hash}.png`.
   - `curl -X POST http://localhost:8080/api/printful/products -H 'Content-Type: application/json' -d '{"text":"FOO","middletext":"BAR"}'` — without `PRINTFUL_TOKEN` set, returns `503` with body `{"error":"printful_unconfigured","file_id":"...","file_url":"..."}`. With a valid token + `PUBLIC_BASE_URL`, returns `{"file_id","file_url","sync_product_id","external_id","mockup_task_id","mockup_status_url"}`.
   - `curl http://localhost:8080/api/printful/mockup/{task_id}` polls a mockup task; pass-through to Printful with the bearer token attached server-side.
   - `curl -X POST http://localhost:8080/api/printful/mockup -H 'Content-Type: application/json' -d '{"text":"FOO","middletext":"BAR"}'` kicks off a standalone mockup (no sync product); useful for dev previews.
   - `curl -X POST http://localhost:8080/api/checkout/start -H 'Content-Type: application/json' -d '{"text":"FOO","middletext":"BAR","variant_id":4012}'` — orchestrates render → Printful sync_product → Stripe Checkout Session in one call. Returns `{checkout_url, session_id, sync_product_id, file_id}`. Without `STRIPE_SECRET_KEY` set, returns 503 with `{"error":"stripe_unconfigured"}`; without `PRINTFUL_TOKEN` returns 503 with `{"error":"printful_unconfigured"}`. Printful failure mid-call short-circuits before Stripe; Stripe failure surfaces the orphan `sync_product_id` so an operator can correlate.
   - `/api/stripe/webhook` accepts signed `checkout.session.completed` events from Stripe and places a Printful order with `confirm=true`. In dev, run `stripe listen` in a second terminal (see below) to forward live webhook events to localhost.

### Stripe Checkout integration

The buy flow on the page POSTs to `/api/checkout/start`, which is a single endpoint that owns the chain: render the print PNG → create the Printful sync product → mint a Stripe Checkout Session with inline `price_data` → return the URL for the browser to redirect to. The hosted Checkout page is the only post-buy view in V1 — no inline Payment Element, no intermediate mockup-preview screen. After payment, Stripe redirects to `/?session_id=...` where `script.js` swaps to a static thanks state with a "Make another →" CTA back to `/`.

Webhook delivery (`POST /api/stripe/webhook`) is signature-verified using `STRIPE_WEBHOOK_SECRET` (1 MiB body cap, default 5-minute tolerance). Idempotency lives in two layers: an in-memory `sync.Map` keyed on `session.id` for tight-window duplicates, and Printful's `external_id = session.id` check as the durable backstop. Auto-confirm is on (`POST /store/orders?confirm=true`) — the moment Stripe says paid, Printful starts fulfillment. The signed-event gate is what keeps forged events from triggering real shipments.

**Why this shape (not pre-created Stripe Products):** every design is unique, so pre-creating `Product` records would add a write per order with zero reuse. Stripe documents `price_data` as the right pattern for one-off, ad-hoc prices. Worldwide shipping is free (baked into the $30 unit_amount), so there's no `shipping_options` line item.

**Why one consolidated endpoint (not chained client calls):** the server owns the chain, so the user gets one click → redirect. If Printful fails, Stripe is never called; if Stripe fails after Printful succeeded, the response carries `sync_product_id`/`file_id` so an operator can correlate the orphan.

**Test/live gate:** `STRIPE_MODE=test|live` is asserted at startup against the secret-key prefix. A `sk_live_*` key with `STRIPE_MODE=test` (or vice versa) refuses to construct the Stripe client, the route 503s, and the misconfiguration is logged loudly. The dangerous direction is the live-key-in-dev case; this is the guardrail.

#### Local dev with `stripe listen`

The Stripe CLI forwards webhook events from the test environment to localhost. Two terminals:

```
# terminal 1
./server/tools/run-dev.sh

# terminal 2
stripe listen --forward-to localhost:8080/api/stripe/webhook
# prints `whsec_...` once at startup. Copy it into .env as
# STRIPE_WEBHOOK_SECRET, then restart terminal 1.
```

Use Stripe **test** keys only in dev (`sk_test_...`). Card `4242 4242 4242 4242`, any future expiry, any CVC. To simulate the webhook without a full e2e click-through, `stripe trigger checkout.session.completed`.

The `VariantID` placeholders in [server/internal/printful/catalog.go](server/internal/printful/catalog.go) and the matching catalog map in [index.html](index.html) are `0` and need to be filled in by hand from `GET /products/71` against your Printful account before end-to-end checkout can succeed; until then, `/api/checkout/start` returns 503 with `{"error":"variant_catalog_incomplete"}` and the front-end shows a "shirt sizes are not configured yet" error.

### Printful integration

The `/api/printful/*` routes need both a bearer token AND a public URL Printful can fetch the print PNG from. Either:

- **Local with tunnel:** start an ngrok or cloudflared tunnel pointing at `:8080`, then run with `PRINTFUL_TOKEN=... PUBLIC_BASE_URL=https://your-tunnel.ngrok.app go run ./cmd/server`. Printful will fetch the print file via that URL.
- **Deployed:** point `PUBLIC_BASE_URL` at your public HTTPS hostname.

When `PRINTFUL_TOKEN` is unset, every `/api/printful/*` route returns 503 with the `file_id`/`file_url` of the saved design so the UI still degrades gracefully — the render path is independent of Printful.

The default catalog is the Bella+Canvas 3001 unisex tee, white, S/M/L/XL — defined in [server/internal/printful/catalog.go](server/internal/printful/catalog.go). The `VariantID` placeholders are `0` and need to be filled in by hand from `GET /products/71` against your Printful account; until then `POST /api/printful/products` will surface a 502 with the upstream 422 message.

Run `go test ./...` from [server/](server/) for unit and golden-file tests.

## Architecture

- **Two pieces today.** The static site at the repo root is the GitHub Pages deploy that powers `thankyoubag.online`. The Go server in [server/](server/) embeds and re-serves that same site, plus adds a render API. They are not deployed together yet — the cutover is deferred.

- **Server-side SVG render (the load-bearing decision).** The browser builds a preview SVG and rasterises via Canvas in [script.js](script.js); the server expands a near-identical Go `text/template` SVG and rasterises with [resvg-go](https://github.com/kanrichan/resvg-go). The client POSTs `{text, middletext}` and the server returns `{file_id, url}`. Three reasons the render moved to the server:

  - Determinism across devices and zoom levels — browser canvas resampling drifts.
  - Content-addressed file URLs that survive page reloads and shareable links.
  - Trust boundary — fulfillment artifacts shouldn't come from untrusted client bytes.

- **Why Go.** Glue server, not a render engine. Fast compile loop, single static binary, `//go:embed` ships the static site and woff alongside the executable, and stdlib `net/http` with Go 1.22+ method patterns is enough for three routes (see [server/internal/httpserver/router.go](server/internal/httpserver/router.go)).

- **Why `resvg-go`.** Wraps Rust's `resvg` via wasm — best-quality SVG renderer reachable from Go. The font database is fed the decoded font once at boot. Render calls are serialised through a mutex because the wasm renderer holds per-call state and is not concurrency-safe. [Helvetica-Black.woff](Helvetica-Black.woff) is decompressed to TTF in-process at startup because resvg's font db doesn't accept WOFF directly. See [server/internal/render/render.go](server/internal/render/render.go) and [server/internal/render/woff.go](server/internal/render/woff.go).

- **Hash-keyed file store.** SHA-256 over canonicalised inputs (NFC-normalised, uppercased, with a template version tag). Same design always returns the same `file_id`. [`singleflight`](https://pkg.go.dev/golang.org/x/sync/singleflight) dedupes concurrent identical requests so 20 simultaneous clicks for the same design produce one render and one disk write. Atomic temp-file rename keeps readers from seeing partial writes. The served PNG carries `Cache-Control: public, max-age=31536000, immutable` since content-addressed URLs are stable by definition. See [server/internal/files/store.go](server/internal/files/store.go).

- **Output dimensions.** Fixed 3600x4800 px (12"x16" at 300 DPI) — picks the Bella+Canvas 3001 DTG print area now so files are Printful-ready when that integration lands. ViewBox width is computed per-input from the longer of `MainText` and `MiddleText` so short inputs get a tight crop. Two implementation deviations from earlier plan drafts worth flagging:

  - `font-size=320` in the template (rather than `20em`) — mirrors the browser's `20em` at a 16px root and gives resvg a concrete number it can size against.
  - A per-input dynamic viewBox width (rather than a fixed `4096x3200`) — keeps short inputs cropped tightly after rasterisation, mirroring the browser's `getBBox` behavior.

  See [server/internal/render/template.go](server/internal/render/template.go) and [server/internal/render/template.svg](server/internal/render/template.svg).

- **HTTP surface.** Routes wired in [server/internal/httpserver/router.go](server/internal/httpserver/router.go):

  - `GET /healthz` — liveness check.
  - `POST /api/render` — validate inputs, hash, render, return `{file_id, url}`.
  - `GET|HEAD /api/files/{hash}.png` — stream the saved PNG with the immutable cache header.
  - `POST /api/printful/products` — render+save, then parallel mockup-task POST + sync-product GET-then-POST against Printful; merged response includes `mockup_status_url` for client polling.
  - `GET /api/printful/mockup/{task_id}` — pass-through proxy that polls Printful with the bearer token server-side.
  - `POST /api/printful/mockup` — standalone mockup-only kickoff (accepts `{file_id}` or `{text, middletext}`).
  - `POST /api/checkout/start` — render+save → Printful sync_product → Stripe Checkout Session, returning `{checkout_url, session_id, sync_product_id, file_id}` for the browser to redirect to.
  - `POST /api/stripe/webhook` — receives signed `checkout.session.completed` events and places a Printful order with `confirm=true`. Signature-verified, idempotent on `session.id`.
  - `/` — static fall-through serving the embedded site.

  Validation, JSON shaping, body-size limits, and cache headers live in [server/internal/httpserver/handlers.go](server/internal/httpserver/handlers.go); the Printful orchestration in [server/internal/httpserver/printful_handlers.go](server/internal/httpserver/printful_handlers.go); the Stripe Checkout flow in [server/internal/httpserver/checkout_handlers.go](server/internal/httpserver/checkout_handlers.go) and [server/internal/httpserver/stripe_webhook.go](server/internal/httpserver/stripe_webhook.go).

- **Print file by URL.** Printful's API GETs the print PNG from the URL we hand it, so the file URL on the wire to Printful must be publicly reachable — `PUBLIC_BASE_URL + /api/files/{hash}.png`. The browser keeps seeing the relative URL; the server constructs the absolute URL only for the Printful payload. See [server/internal/httpserver/printful_handlers.go](server/internal/httpserver/printful_handlers.go) `publicFileURL`.

- **Sync-product idempotency.** `external_id = "tyb-" + file_id[:12]` — deterministic from the design. The handler does `GET /store/products/@{external_id}` first; on 404 it POSTs to `/store/products`; on 200 it reuses. A `singleflight` keyed on the external_id collapses concurrent identical creates so the race-on-404 doesn't double-create. See [server/internal/printful/products.go](server/internal/printful/products.go).

- **Parallel Printful fanout.** Mockup-task creation and sync-product creation are independent given the same print PNG, so they run in parallel goroutines via `errgroup`. Partial failures (one succeeded, the other failed) return 502 with a `partial:{...}` block describing what survived; the client retries the failed half.

- **What's deferred.** Multi-product UI (more than the Bella+Canvas 3001 tee), refund automation, mockup preview on the Stripe Checkout product image (today the rendered PNG is shown — see the V2 note in [the TYB-12 plan](.lattice/plans/task_01KQWEW3DBADPSAXZXC5R0XGMX.md)), and the deploy + DNS cutover from GitHub Pages to the Go server.

## Repo layout

The GitHub Pages site sits at the repo root:

- [index.html](index.html), [style.css](style.css), [script.js](script.js) — the page and its preview/canvas logic.
- [Helvetica-Black.woff](Helvetica-Black.woff), [Helvetica-Black.woff2](Helvetica-Black.woff2) — the display font, shared verbatim with the server.
- [splash.png](splash.png), [favicon.ico](favicon.ico), [CNAME](CNAME) — splash image, favicon, and the GitHub Pages custom-domain pointer.

The Go server lives under [server/](server/):

- [server/cmd/server/main.go](server/cmd/server/main.go) — entry point, env wiring, signal handling.
- [server/internal/render/](server/internal/render/) — input validation, hashing, SVG template expansion, resvg rasterisation.
- [server/internal/httpserver/](server/internal/httpserver/) — router, handlers, embedded-static FS, Printful orchestration.
- [server/internal/files/](server/internal/files/) — content-addressed PNG store with singleflight dedup and atomic writes.
- [server/internal/printful/](server/internal/printful/) — typed HTTP client for Printful's mockup-task, sync-product, and order endpoints; default catalog config.
- [server/internal/stripe/](server/internal/stripe/) — thin wrapper around `stripe-go/v82` exposing `CreateCheckoutSession` and `VerifyWebhook`; STRIPE_MODE=test|live startup gate.
- [server/tools/copy-static.sh](server/tools/copy-static.sh) — refresh the embedded static FS from the repo root.
- `server/data/files/` — runtime PNG output (gitignored).

The [.lattice/](.lattice/) directory holds plans, tasks, and session events for ongoing work.

## Tasks tracked in Lattice

This project uses [Lattice](.lattice/) for file-based, event-sourced task tracking. Run `lattice list` (or read [.lattice/plans/](.lattice/plans/)) for current state — see [CLAUDE.md](CLAUDE.md) for the workflow.
