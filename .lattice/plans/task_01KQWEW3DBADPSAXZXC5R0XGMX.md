# TYB-12: Stripe Checkout integration for buy-now flow

## Summary

Single click takes the customer from designed-tee to Stripe Checkout. On the
"Buy a Shirt" click, a new server endpoint **renders the PNG → creates the
Printful sync_product → creates a Stripe Checkout Session → returns the
checkout URL** as one atomic operation. The browser redirects to Stripe on
success, or shows an inline error message describing which step failed.
After payment, the `checkout.session.completed` webhook fires and the
server places a Printful order against the previously-created
sync_product_id. No new Stripe Products/Prices: every design is unique, so
prices are inlined per Session via `price_data`. Single line item, USD,
shipping address collected by Stripe, sync_product_id + variant_id carried
through Session metadata into the webhook → order step.

Mockup generation is **decoupled from the buy flow.** The Stripe Checkout
product image uses the rendered PNG (`file_url`), not the mockup — the
customer just designed it and knows what it looks like, and waiting on
Printful's mockup task (5–30s typical) would make the buy click feel slow.
Mockup polling/display remains a separate concern, useful for any preview
flow but not gating on checkout.

## End-to-end flow

```
[browser]                  [Go server]                [Printful]           [Stripe]
    |                           |                         |                    |
    | (1) user picks size on the design page (default M)                       |
    |                                                                          |
    | (2) clicks "Buy a Shirt"                                                 |
    |                                                                          |
    | (3) POST /api/checkout/start                                             |
    |     {text, middletext, variant_id}                                       |
    |-------------------------->| (a) render+save PNG                           |
    |                           |     -> {file_id, file_url}                   |
    |                           | (b) create sync_product                      |
    |                           |     POST /store/products ---->|              |
    |                           |     <----- {sync_product_id, sync_variants}  |
    |                           |     (on failure: 502, return early           |
    |                           |      with {error:"printful_create_failed"})  |
    |                           | (c) create checkout session                  |
    |                           |     POST /v1/checkout/sessions -------->|    |
    |                           |     <----- {id, url} -------------------|    |
    |                           |     (on failure: 502, return early          |
    |                           |      with {error:"stripe_session_failed",   |
    |                           |      sync_product_id} so the operator can   |
    |                           |      reconcile or clean up)                 |
    |<--{checkout_url, file_id, sync_product_id}                              |
    |                                                                          |
    | (4) window.location = checkout_url                                       |
    |                           ----- user pays on stripe.com -----            |
    |                                                                          |
    | (5) Stripe redirects to /thanks?session_id={CHECKOUT_SESSION_ID}         |
    |                                                                          |
    | (6) async — Stripe POSTs webhook                                         |
    |                           |<-- POST /api/stripe/webhook ----------------|
    |                           | verify signature                             |
    |                           | event=checkout.session.completed             |
    |                           | idempotency: SeenSession(session.id) ?       |
    |                           |   yes -> 200 noop                            |
    |                           |   no  -> mark seen, place order              |
    |                           |--- POST /store/orders?confirm=true ->|       |
    |                           |    { external_id: session.id,                |
    |                           |      recipient: shipping_details,            |
    |                           |      items: [{ sync_variant_id }],           |
    |                           |      retail_costs: … }                       |
    |                           |<--- {order id, status: "pending"}            |
    |                           | 200 to Stripe                                |
    |                                                                          |
    | (7) /thanks page renders static "Thanks — make another →" CTA            |
```

Data residency at each step:
- `text`, `middletext`, `variant_id`: collected from the design page form
  state, sent in step (3).
- `file_id`, `sync_product_id`: minted by the server during step (3),
  echoed to the browser for diagnostic display only — they're also
  embedded in the Stripe Session metadata so the webhook handler can
  recover them without trusting the client.
- Shipping address: never seen by the browser. Collected by Stripe's
  hosted page, surfaced to the server only on the webhook payload.
- Session id: minted by Stripe in step (3); becomes the idempotency key
  for Printful order creation (`external_id`) in step (6).

Why one consolidated endpoint (vs. two chained client calls):
- One network hop from the browser; the server owns the chain so the user
  experiences a single click → redirect.
- Atomic error story: if Printful fails, we never call Stripe; the user
  sees "couldn't create your shirt" and retries. If Stripe fails after
  Printful succeeded, the orphan sync_product is cheap (Printful charges
  nothing for unfulfilled designs); the operator can prune via the
  Printful dashboard or a future cleanup job.
- No risk of a half-succeeded buy where the client crashes between two
  POSTs.

Why this shape (vs. inline Payment Element):
- Out-of-scope per task description.
- Hosted Checkout is one network call (server→Stripe) and one webhook —
  minimum architectural surface for the same outcome. PCI/SCA/3DS handled
  by Stripe's UI.

Why the Stripe Checkout product image is the rendered PNG, not the mockup:
- Printful's mockup task is async (5–30s typical) and would gate the buy
  click on it. Bad UX for a one-click flow.
- The customer just designed the bag and is staring at a preview already;
  showing the design rendering on the Stripe page is informative enough.
- We can revisit this in V2: kick off the mockup in parallel inside
  `/api/checkout/start`, store the task id on the Session metadata, and
  have the webhook handler optionally backfill the product image after the
  fact (or just include it in the Printful order). Out of scope for V1.

## Stripe object model: inline `price_data`, no pre-created Products/Prices

**Decision:** every Checkout Session uses `line_items[].price_data` inline.
Don't create Stripe Product/Price records.

Reasons:
- The product is uniquely-designed per customer. Pre-creating Products would
  add a write to Stripe per design, with zero reuse — Stripe itself
  documents `price_data` as the right pattern for "ad-hoc, one-off prices."
- Idempotency is already handled at the Session layer (we use
  `idempotency_key = sync_product_id + variant_id + nonce`); no need for the
  Product API to dedupe anything.
- Avoids two-step sync between Printful sync_products and Stripe Products.

Session creation parameters (minimal viable set):

```
mode = "payment"
line_items = [{
  quantity: 1,
  price_data: {
    currency: "usd",
    unit_amount: <cents — see "Pricing source of truth">,
    product_data: {
      name: "Thank You Bag Tee — <design summary>",
      images: [<absolute mockup_url if available, else file_url>],
      metadata: {sync_product_id, variant_id, file_id}
    }
  }
}]
shipping_address_collection = {allowed_countries: <full list of Printful-supported ISO codes>}
// shipping_options omitted — free worldwide shipping baked into unit_amount
phone_number_collection = {enabled: true}                      // Printful asks for it
billing_address_collection = "auto"
metadata = {
  sync_product_id, variant_id, file_id, external_id,           // for webhook
  app: "thankyou", schema: "v1"
}
success_url = "<PUBLIC_BASE_URL>/thanks?session_id={CHECKOUT_SESSION_ID}"
cancel_url  = "<PUBLIC_BASE_URL>/?canceled=1"
```

Note `{CHECKOUT_SESSION_ID}` is a literal placeholder Stripe substitutes — do
not URL-encode it.

## Variant + shipping address

**Variant selection:** the client owns it, picked **before** the buy
click. Plan:

1. Add a size picker (radio buttons or `<select>` for S/M/L/XL) inline
   with the existing design controls. Visible from page load.
2. Default to M. Persist selection across sessions via `localStorage` or
   the existing `searchParams` URL state — minor UX polish.
3. Sent in the `/api/checkout/start` POST as `{text, middletext,
   variant_id}`.

We do not use Stripe's `custom_fields` for size. Two reasons:
- We need the size before Session creation to compute `unit_amount`
  (Printful retail price varies by variant) and to map to a
  `sync_variant_id` for the Printful order.
- Stripe `custom_fields` come back via the Session retrieve, complicating
  the webhook → order mapping for no UX win.

**Shipping address:** Stripe collects via `shipping_address_collection`.
The webhook payload exposes it on `session.customer_details.address` and
`session.shipping_details.address`. Map fields straight into the Printful
recipient struct (full mapping described under "Server changes — orders").

**Allowed countries: worldwide.** User decision: ship anywhere Printful
supports. The `allowed_countries` list will mirror Printful's supported
country set. Implementation: maintain a small static list in
`internal/printful/catalog.go` (e.g., `SupportedCountries []string`)
sourced from Printful's docs; the create-session handler reads it and
passes it to Stripe. Revisit if/when Printful adds or drops a country.

**Shipping rates:** free worldwide. Built into the `$30` unit_amount —
**no** Stripe `shipping_options` line item. (Confirmed: we eat the
shipping cost.) Stripe Tax is out of scope.

## Webhook design

**Endpoint:** `POST /api/stripe/webhook`. Wired in the router. Handler reads
the raw body (must NOT json-decode it before signature verify), reads the
`Stripe-Signature` header, and calls `webhook.ConstructEvent(body, sigHeader,
STRIPE_WEBHOOK_SECRET)` from the Stripe Go SDK.

**Event subscribed:** `checkout.session.completed`. Reasons over alternatives:
- `payment_intent.succeeded` — fires before the Session is finalized; harder
  to retrieve the metadata and shipping_details cleanly.
- `checkout.session.async_payment_succeeded` — only for delayed payment
  methods (ACH, etc.). We don't enable those in V1, so we can ignore it. If
  we do enable them, listen to it as a peer event.

We accept that `checkout.session.completed` fires even for `payment_status:
"unpaid"` if a delayed-payment method is used; the handler must check
`session.payment_status == "paid"` before placing the Printful order.

**Signature verification:** mandatory. Reject with 400 on any failure.
Constant: maximum tolerance `300s` (Stripe's default). Body size limit:
1 MiB cap (Stripe payloads are < 30 KiB; cap defends against a hostile
proxy).

**Idempotency:** Stripe will retry up to 3 days on non-2xx. The Printful
order creation must be idempotent on `session.id`. Two-layered approach:

1. **In-memory seen-set** (sync.Map keyed by `session.id`). Stops duplicate
   processing in the same process within seconds of each other. Cheap.
2. **Printful `external_id` = `session.id`**. Printful's `POST /store/orders`
   accepts an optional `external_id`; on duplicate, Printful returns 409 (or
   200 with the existing order — confirm via implementation). The handler
   treats both as success. This is the durable layer that survives restarts
   and concurrent webhook retries across process boundaries.

We do NOT persist a local SQLite/JSON ledger of fulfilled sessions in V1.
Adding storage for one feature isn't worth the complication; Printful's
external_id check is sufficient. Documented as a known limitation: if
Printful is down for >3 days while Stripe retries, we may drop an order;
acceptable risk for prototype traffic.

**Failure handling:**

| Stage | Outcome | Webhook response | Operator action |
|-------|---------|------------------|-----------------|
| Sig verify fails | invalid request | 400 | none — Stripe stops retrying after a few attempts |
| Stripe metadata missing (sync_product_id) | unrecoverable | 200 + log loud | manual refund via Stripe dashboard |
| Printful 5xx | transient | 5xx (Stripe retries) | watch logs |
| Printful 4xx (variant invalid, etc.) | unrecoverable | 200 + log loud | manual refund + investigate |
| Printful 409 / duplicate external_id | already-placed | 200 | none |

Manual refunds via Stripe dashboard are the V1 escape hatch. Automated
refund-on-failure is out of scope (it adds a second writer to Stripe and
needs careful idempotency itself).

## Server changes

### New package: `server/internal/stripe/`

Mirror the structure of `internal/printful/`:

- `client.go` — thin wrapper around `github.com/stripe/stripe-go/v82`.
  Constructor: `New(Config{SecretKey, WebhookSecret, Logger}) (*Client, error)`.
  Returns `ErrMissingKey` if SecretKey is empty (mirrors `ErrMissingToken`).
  Methods: `CreateCheckoutSession(ctx, params) (*stripe.CheckoutSession, error)`
  and `VerifyWebhook(payload []byte, sigHeader string) (*stripe.Event, error)`.
- `client_test.go` — `httptest`-mounted tests against an injectable HTTP
  client, mirroring `printful/client_test.go`.
- `errors.go` — typed errors (`ErrMissingKey`, `ErrInvalidSignature`).
- (No `catalog.go` — pricing is computed at handler layer from the Printful
  catalog map.)

The Stripe SDK supports a `*http.Client` injection point (via
`stripe.SetHTTPClient` or a per-request backend). Use that for tests; mirror
the `BaseURL` test pattern in printful/client.go so we mount an
`httptest.Server` and inject it.

### New handler: `server/internal/httpserver/checkout_handlers.go`

This is the orchestration handler — it stitches existing render +
Printful flows together with the new Stripe Session step. The webhook
handler lives separately (see below).

#### `POST /api/checkout/start`

Request body:
```json
{
  "text": "THANK YOU",
  "middletext": "ENJOY",
  "variant_id": 4012
}
```

Steps:
1. Validate inputs: `text` non-empty after the existing canonicalization,
   `variant_id` in the configured catalog
   (`printful.DefaultVariantIDs()`).
2. Render+save the PNG via the existing `render` package
   (`renderer.Render(ctx, text, middletext)` → `{file_id, file_url}`).
   Reuse the same canonicalization + dedupe path as `/api/render` so a
   re-click on the same design returns the same `file_id`.
3. Create the Printful sync_product via the existing `printful.Client`
   (the same call `/api/printful/products` makes today). On any error
   from Printful (4xx or 5xx), return `502` with body
   `{error:"printful_create_failed", detail, file_id, file_url}`. Do
   NOT call Stripe.
4. Compute `unit_amount` (cents) from the variant's `RetailPrice` in
   `printful.DefaultVariants` (string "30.00" → 3000) — or
   `STRIPE_PRICE_USD_CENTS` env override if set. Server is the source of
   truth; never trust a client-supplied price.
5. Build Stripe Session params (see "Stripe object model" above).
   `images` uses the absolute `file_url` of the rendered PNG (build with
   `PUBLIC_BASE_URL` so Stripe can fetch it).
6. Set Stripe idempotency key to a hash of the inputs + a
   monotonic-ish nonce so accidental double-clicks resolve to one
   Session. Specifically: `sha256(file_id + variant_id + client_ip +
   60-second-window)[:32]`. The `file_id` is content-addressed, so this
   naturally dedupes identical-design double-clicks.
7. Call `stripeClient.CreateCheckoutSession(ctx, params)`. On error,
   return `502` with body `{error:"stripe_session_failed", detail,
   sync_product_id, file_id}` so the client can show a useful message
   and the operator can correlate the orphan sync_product.
8. Return `200 {checkout_url: session.URL, session_id: session.ID,
   sync_product_id, file_id}`.

Status codes:
- `200` — success, body has `checkout_url`.
- `400` — validation (missing text, bad variant_id).
- `502` — upstream failure: body distinguishes `printful_create_failed`
  vs `stripe_session_failed` so the client can render a specific error.
- `503` — either Printful or Stripe unconfigured. Body includes
  `{error:"printful_unconfigured"}` or `{error:"stripe_unconfigured"}`
  matching the existing Printful pattern.

Note: this handler **partially overlaps** with the existing
`/api/printful/products` route — both render+save and create
sync_products. We keep `/api/printful/products` for any other consumer
(e.g., the existing UI flow showing a mockup preview) but the buy flow
calls the new endpoint exclusively. Common code lives in
`internal/printful/sync.go` (extract the create-sync-product helper)
and `internal/render` (already exists).

#### `POST /api/stripe/webhook`

Steps:
1. Cap body at 1 MiB via `http.MaxBytesReader`.
2. Read full body into memory (Stripe's `webhook.ConstructEvent` needs the
   raw bytes).
3. Verify signature with `STRIPE_WEBHOOK_SECRET`. On failure → 400.
4. Switch on `event.Type`. V1 only handles `checkout.session.completed`;
   every other type returns 200 (we silently ack so Stripe stops retrying;
   logging the type is enough to spot misconfiguration).
5. Unmarshal `event.Data.Raw` into `stripe.CheckoutSession`.
6. Idempotency check (`SeenSessions.LoadOrStore(session.ID)`). If already
   seen, return 200 immediately.
7. If `session.PaymentStatus != "paid"`, log and return 200 (delayed
   payment — wait for the async event; not used in V1).
8. Pull metadata: `sync_product_id`, `variant_id`, `file_id`. If any
   missing/malformed, log loudly, return 200 (don't loop Stripe — we can't
   recover automatically).
9. Build the Printful order request (described below) and POST.
10. On Printful 2xx or 409 → 200 to Stripe. On Printful 5xx → 5xx
    response so Stripe retries. On Printful 4xx (other) → 200 + alert log.

### New code in `internal/printful/orders.go` (V2 — implementation)

Add a thin order-creation method:

- `CreateOrder(ctx, CreateOrderRequest) (OrderData, error)` —
  `POST /store/orders?confirm=true`. Auto-confirm is the V1 default
  (user-decided): the moment Stripe confirms payment, Printful starts
  fulfillment. Tradeoff: a webhook bug becomes a real-money / real-shirt
  incident. Mitigations: webhook signature verification rejects forgeries
  (signed events only), idempotency via `external_id = session.id`
  prevents duplicate orders, and the unit tests cover the missing-
  metadata and not-paid paths so unparseable events never reach this
  call.
- Carry `external_id = session.id` so Printful's idempotency does the heavy
  lifting on retries.

Map `sync_product_id + variant_id` → `sync_variant_id` by looking up the
sync_variants array on the parent product. Either:
- (a) Cache the sync_variants returned at sync_product creation time.
- (b) Re-fetch via `GET /store/products/@{external_id}` lazily in the order
  handler.

Recommend (b) for V1 — it's one extra Printful call per webhook, but avoids
adding a server-side store. The webhook is async, so latency doesn't hurt
anyone.

Recipient struct mapping from Stripe:

| Stripe field | Printful recipient field |
|--------------|--------------------------|
| `customer_details.name` | `name` |
| `shipping_details.address.line1` | `address1` |
| `shipping_details.address.line2` | `address2` |
| `shipping_details.address.city` | `city` |
| `shipping_details.address.state` | `state_code` (USPS abbrev — Stripe returns this for US) |
| `shipping_details.address.country` | `country_code` |
| `shipping_details.address.postal_code` | `zip` |
| `customer_details.phone` | `phone` |
| `customer_details.email` | `email` |

### Wiring (`router.go` and `main.go`)

**`router.go`:** add two `mux.HandleFunc` lines next to the Printful ones:

```go
mux.HandleFunc("POST /api/checkout/start",   h.StartCheckout)
mux.HandleFunc("POST /api/stripe/webhook",   h.StripeWebhook)
```

**`handlers.go`:** add `Stripe *StripeSetup` to the `Handlers` struct,
mirroring `Printful *PrintfulSetup`. `StripeSetup` holds the
`*stripe.Client`, the `WebhookSecret` (kept on the setup struct, not the
client, since only the webhook handler needs it), and a `SeenSessions
sync.Map` for the idempotency layer.

**`main.go`:** add a `buildStripe(logger)` function paralleling
`buildPrintful`. When `STRIPE_SECRET_KEY` is unset, log a warning and pass
`nil`. When set but `STRIPE_WEBHOOK_SECRET` is unset, log a loud warning
(the create-session route will work; the webhook will reject everything).
Pass the resulting `*StripeSetup` into `httpserver.Handlers`.

### New env vars

Append to `.env.example` (alongside the existing Printful block):

```
# Stripe Checkout integration. The Stripe account is shared with another
# Printful-backed store; this server uses a RESTRICTED key scoped to the
# minimum: "Checkout Sessions: write", "Customers: write", "Events: read".
# Leave STRIPE_SECRET_KEY unset to run without checkout (the
# /api/checkout/start route will 503 with a clear error so the rest of the
# UI degrades gracefully).
STRIPE_SECRET_KEY=
# Webhook signing secret from `stripe listen` (test) or the dashboard's
# webhook endpoint settings (live). Required for /api/stripe/webhook.
STRIPE_WEBHOOK_SECRET=
# Required: "test" or "live". Asserted at startup against STRIPE_SECRET_KEY's
# prefix (sk_test_* / sk_live_* / rk_test_* / rk_live_*). Mismatch fails fast
# with a loud error — prevents accidentally pasting a live key in dev.
STRIPE_MODE=test
# Optional: override the default per-tee unit price (in USD cents). Falls back
# to the variant's RetailPrice from internal/printful/catalog.go * 100
# (currently $30.00 → 3000).
STRIPE_PRICE_USD_CENTS=
```

The 503-degradation pattern mirrors Printful exactly.

## Frontend changes (`script.js` + `index.html`)

### `index.html`

Add a size picker visible from page load, near the existing
`#buy-shirt` button. Recommended: a `<fieldset>` of radio buttons (S/M/L/XL)
labelled "Size", styled to match the existing controls. Default = M.

The button text remains "Buy a Shirt" (no separate "Buy Now" step in the
new flow). On click, the handler in `script.js` is repointed at the new
endpoint.

For the thanks page: handle the `?session_id=` query param at `init()`
and swap `document.body.innerHTML` to a thanks state. No new HTML routes
needed — the existing `/` static handler serves the page, the JS reads
`searchParams`. Same pattern is used for `?canceled=1`.

### `script.js`

The existing `createTShirt` function is **rewritten** to call the new
endpoint. The mockup-poll path (`pollMockup`, `renderSuccess`,
`updateMockupStatus`, `extractMockupURL`) is **removed from the buy flow**
— it remains as dead code we delete in this task, since the only caller
is the buy flow.

New `createTShirt`:

```js
async function createTShirt() {
  if (renderInflight) return;
  renderInflight = true;
  const button = document.getElementById('buy-shirt');
  if (button) button.classList.add('is-loading');
  const main = document.querySelector('#main-input').value || '';
  const middle = document.querySelector('#highlight-input').value || '';
  const variantID = readSelectedVariantID(); // from the new size picker
  try {
    const resp = await fetch('/api/checkout/start', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ text: main, middletext: middle, variant_id: variantID }),
    });
    let data = null;
    try { data = await resp.json(); } catch (_) { data = {}; }

    if (resp.status === 503) {
      showError(data.error === 'stripe_unconfigured'
        ? 'Checkout is not configured yet. Try again later.'
        : 'Shop is not fully configured yet. Try again later.');
      return;
    }
    if (resp.status === 502) {
      if (data.error === 'printful_create_failed') {
        showError("Couldn't create your shirt. Please try again.");
      } else if (data.error === 'stripe_session_failed') {
        showError("Couldn't start checkout. Please try again.");
      } else {
        showError("Something went wrong. Please try again.");
      }
      return;
    }
    if (!resp.ok) {
      showError(data.message || data.error || ('HTTP ' + resp.status));
      return;
    }
    window.location = data.checkout_url;
  } catch (e) {
    console.error('createTShirt error', e);
    showError('Network error. Please try again.');
  } finally {
    renderInflight = false;
    if (button) button.classList.remove('is-loading');
  }
}
```

Where `showError(msg)` injects a non-blocking inline message near the
button (replaces today's `alert()` calls — alert is blocking and feels
broken on mobile). A small `<div id="buy-error">` reserved in the markup,
toggled visible on error. Auto-clears on the next click.

`readSelectedVariantID()` reads from the new size picker; returns the
numeric variant_id from the static catalog the page knows about (small
JSON object embedded at page load via a `<script>` tag, OR fetched once
from a new `GET /api/catalog` route — recommend the embedded option for
V1, no extra route needed).

### Thanks page

Pure JS: at `init()`, check `searchParams.get('session_id')`. If present,
render a static thanks state:

- Headline: "Thanks — your order is on its way."
- A clear CTA back to the home page: "Make another →" linking to `/`.
- Subtle nod to the design they just bought (optional V2 — fetch the
  Printful order via a `GET /api/checkout/session/{id}` route to show the
  mockup). For V1, copy alone is fine.

The CTA must be the primary visual action — the goal is to encourage repeat
orders.

For `?canceled=1`, render a "Checkout canceled — try again" message that
preserves the design state in URL params.

## Pricing source of truth

**Server-side, computed from `printful.DefaultVariants[].RetailPrice` × 100
(cents).** Override via `STRIPE_PRICE_USD_CENTS` env var if set.

User-confirmed retail price: **$30 flat across S/M/L/XL** (free shipping
baked in). As part of this task, update
`internal/printful/catalog.go`'s `DefaultRetailPrice` from `"25.00"` to
`"30.00"` so Stripe and Printful share one source. The env var override
remains for ad-hoc adjustments without re-deploying the catalog.

Reasons for keeping catalog as source of truth:
- The variant's retail price already lives in `internal/printful/catalog.go`.
  Re-using it keeps Printful and Stripe in lockstep without a second
  source-of-truth tier.
- Tying the line-item price to the variant means future per-size pricing
  (e.g., XL costs more) Just Works.
- Pulling from the Printful sync_product API at session-creation time is
  feasible but adds latency and an upstream coupling we don't need (the
  catalog is static for V1).

This still requires the human to fix the placeholder `VariantID = 0` values
in `catalog.go` before end-to-end checkout works (already a flagged blocker
for the existing Printful integration).

## Local dev story

Stripe CLI is the only new dev tool:

1. Install: `brew install stripe/stripe-cli/stripe`.
2. Authenticate once: `stripe login`.
3. Two terminals during dev:
   ```
   # terminal 1
   ./server/tools/run-dev.sh

   # terminal 2
   stripe listen --forward-to localhost:8080/api/stripe/webhook
   # prints "whsec_..." — copy into .env as STRIPE_WEBHOOK_SECRET, restart server.
   ```
4. Use Stripe test keys (`sk_test_...`) — never live keys for dev.
5. Test card: `4242 4242 4242 4242`, any future expiry, any CVC.
6. Trigger one-shot for handler dev: `stripe trigger checkout.session.completed`.

`run-dev.sh` doesn't need changes — it sources `.env` and any
`STRIPE_*` keys flow through. Document the two-terminal flow in `README.md`.

To make the "two terminals" feel less janky, we *could* add a
`tools/run-dev-with-stripe.sh` wrapper using a `concurrently`-style trick (or
two backgrounded processes with traps). Defer to V2 — the explicit two
terminals are clearer for now.

Test vs live keys: distinguished by their prefixes (`sk_test_*` / `rk_test_*`
vs `sk_live_*` / `rk_live_*`). **`STRIPE_MODE` env var (required:
`test|live`) is asserted at startup.** The startup gate, in
`internal/stripe/client.go`'s `New(...)`:

1. If `STRIPE_MODE=test` and the key does not start with `sk_test_` or
   `rk_test_`, return `ErrModeMismatch` and refuse to boot the Stripe
   client (the /api/checkout/start route then 503s).
2. Symmetric check for `STRIPE_MODE=live`.
3. On `live` mode, also log loudly at boot: "Live mode active — real money
   will move."

This makes it impossible to accidentally take a live payment in dev.

## Tests

Mirror `printful_handlers_test.go` patterns — `httptest.Server` stub for
Stripe, real `httptest.NewRecorder` for the inbound side.

### Unit tests (in priority order)

1. **`TestStartCheckoutHappyPath`** — POST with valid `text`,
   `middletext`, `variant_id` → 200 with `checkout_url`. Assert the stub
   Printful server saw `POST /store/products` and the stub Stripe server
   saw a Session create with correct `line_items[0].price_data` and
   `metadata` fields (sync_product_id, variant_id, file_id).
2. **`TestStartCheckoutInvalidVariant`** — POST with `variant_id` not in
   catalog → 400 validation_failed.
3. **`TestStartCheckoutPrintfulFailureNoStripeCall`** — Printful stub
   returns 500 → 502 with `{error:"printful_create_failed"}`. Assert
   stub Stripe was NOT called (counter == 0). Critical: a Printful
   failure must short-circuit before Stripe.
4. **`TestStartCheckoutStripeFailureExposesOrphan`** — Printful stub
   returns 200 but Stripe stub returns 500 → 502 with
   `{error:"stripe_session_failed", sync_product_id, file_id}`. Assert
   the body contains the orphan IDs so an operator can correlate.
5. **`TestStartCheckoutStripeUnconfigured503`** — `STRIPE_SECRET_KEY`
   unset → 503 + `error:"stripe_unconfigured"`. Printful was not called.
6. **`TestStartCheckoutPrintfulUnconfigured503`** — `PRINTFUL_TOKEN`
   unset → 503 + `error:"printful_unconfigured"`. Stripe was not called.
7. **`TestStripeWebhookSignatureValid`** — POST a forged event signed with
   the test webhook secret → 200, verify the stub Printful saw a
   `POST /store/orders` with the right external_id and recipient.
8. **`TestStripeWebhookSignatureInvalid`** — POST a body with a bad signature
   → 400.
9. **`TestStripeWebhookIdempotent`** — POST the same event twice → only one
   Printful order POST.
10. **`TestStripeWebhookUnknownEventType`** — POST a `customer.created` event
    → 200 with a log line, no Printful call.
11. **`TestStripeWebhookPaymentNotPaid`** — POST a session.completed with
    `payment_status: "unpaid"` → 200, no Printful call.
12. **`TestStripeWebhookPrintfulFailure`** — Printful stub returns 500 →
    webhook returns 5xx (so Stripe retries).
13. **Optional `TestStripeWebhookMissingMetadata`** — session without our
    metadata keys → 200 + alert log, no Printful call.

### How to forge a signed Stripe webhook in tests

Stripe Go SDK doesn't ship a public sign helper, but the algorithm is
HMAC-SHA256 over `timestamp + "." + payload`. Add a small `signTestPayload`
helper in the test file that produces `t=<unix>,v1=<hex>` — this matches
how the Stripe SDK examples do it.

### Coverage gaps NOT covered in V1 tests

- A real end-to-end test against Stripe test mode (it's slow, network-bound,
  flaky on CI). Manual e2e verification is in the acceptance criteria.
- Refund flows (out of scope).

### Repo conventions matched

- One test file per handler family (`stripe_handlers_test.go`).
- `httptest.Server` stub injected via `BaseURL` config.
- Atomic counters on stubs (`atomic.Int32`) for "was-called-once" assertions.
- `t.Cleanup` for renderer/server teardown.

## Acceptance criteria

1. **One-click checkout works.** `POST /api/checkout/start` with a valid
   `text`, `middletext`, and `variant_id` returns `200` with a body
   containing `checkout_url` (HTTPS), `session_id`, `sync_product_id`,
   and `file_id`. `curl` verifiable.
2. **Validation rejects nonsense.** Same endpoint with a missing/empty
   `text` or non-catalog `variant_id` returns `400`.
3. **Printful failure short-circuits.** If Printful returns 5xx, the
   endpoint returns `502` with `{error:"printful_create_failed"}` and the
   server NEVER calls Stripe.
4. **Stripe failure surfaces orphan IDs.** If Printful succeeds but Stripe
   fails, the endpoint returns `502` with
   `{error:"stripe_session_failed", sync_product_id, file_id}` so an
   operator can correlate the orphan.
5. **Unconfigured degrades gracefully.** Unset `STRIPE_SECRET_KEY` →
   `503` with `{error:"stripe_unconfigured"}`. Unset `PRINTFUL_TOKEN` →
   `503` with `{error:"printful_unconfigured"}`.
6. **Webhook signature works.** A request to `/api/stripe/webhook` signed
   with `STRIPE_WEBHOOK_SECRET` and carrying a `checkout.session.completed`
   event whose `metadata` includes a valid `sync_product_id` + `variant_id`
   triggers a `POST /store/orders` to Printful with `external_id` ==
   `session.id`. Verifiable in the stub Printful's call log in tests.
7. **Webhook signature rejects forgeries.** Same endpoint with a bad
   signature → `400`.
8. **Webhook is idempotent.** Two consecutive valid webhook deliveries with
   the same `session.id` produce one Printful order create call.
9. **Manual end-to-end with Stripe CLI.** With test keys, `stripe listen`
   running, and a real Printful sandbox token: clicking Buy a Shirt in
   the browser results in (a) Stripe redirect (no intermediate mockup
   step), (b) test-card success, (c) redirect to
   `/thanks?session_id=...`, (d) a draft Printful order visible in the
   Printful dashboard with the right design and shipping address.
10. **Default variant pricing flows through.** The Stripe Checkout UI
    shows $30 (from `printful.DefaultVariants[…].RetailPrice`).
11. **STRIPE_MODE gate works.** Setting `STRIPE_MODE=test` with an
    `sk_live_*` key fails to boot the Stripe client (logged loudly,
    create-checkout-start returns 503).
12. **README updated.** New env vars, the two-terminal `stripe listen`
    dev flow, the buy-flow architecture decision (single
    `/api/checkout/start` endpoint), and the deferred-mockup note are
    documented in `README.md`. The "What's deferred" line is updated to
    remove "Order placement and payments".
13. **No regressions.** All existing Printful tests still pass; `go test
    ./...` from `server/` is green.

## Sequencing & dependencies

Implementation order suggested:

1. Add the `internal/stripe` package + tests (no handlers yet) — builds and
   tests in isolation.
2. Add `internal/printful/orders.go` + tests for `CreateOrder`.
3. Refactor: extract the create-sync-product helper from
   `printful_handlers.go` into `internal/printful/sync.go` so the new
   checkout-start handler can reuse it without duplicating logic. Make
   sure existing `/api/printful/products` tests still pass.
4. Add `checkout_handlers.go` (`/api/checkout/start`) + tests. Wire in
   `router.go` and `main.go`.
5. Add `stripe_webhook.go` (`/api/stripe/webhook`) + tests.
6. Add the frontend size picker + repointed buy button + thanks page.
7. Manual end-to-end via Stripe CLI (acceptance criterion 9).
8. README + `.env.example` updates.

The blocker that already exists for TYB-8 — placeholder `VariantID = 0` in
`catalog.go` — must be resolved before acceptance criterion 9 is testable.
That fix is independent of this task; if it's not done by the time
implementation lands, the implementation can ship behind a 503 with a
"variant catalog incomplete" message in the checkout-start handler.

## Resolved decisions (2026-05-05, from user)

1. **Retail price:** $30 flat S/M/L/XL. Update `DefaultRetailPrice` to "30.00".
2. **Shipping:** free worldwide, baked into the $30. No Stripe
   `shipping_options`.
3. **Allowed countries:** worldwide — every country Printful supports.
4. **Stripe key:** restricted key, write scope on Checkout Sessions +
   Customers + read on Events. Stripe account is shared with another store;
   Printful API key is per-project (not shared).
5. **Test/live gate:** `STRIPE_MODE=test|live` env, asserted at startup
   against the secret-key prefix. Mismatch fails fast.
6. **Thanks page:** static, with a primary "Make another →" CTA back to `/`.
7. **Stripe SDK:** latest `stripe-go` (v82+).

8. **Order confirmation:** auto-confirm. Webhook calls
   `POST /store/orders?confirm=true` so payment immediately triggers
   fulfillment.

## Still-open items

(None — plan is implementation-ready.)
