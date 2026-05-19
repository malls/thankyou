# TYB-23: Buy a shirt should be a separate page

## Goal

Split the buy flow into two pages: the generator (`/`) — text inputs + a Buy button only — and a new checkout page (`/checkout`) — design preview + price + free-shipping copy + size selector + final Buy button. Clicking Buy on the generator navigates to `/checkout?text=...&middletext=...`. The final Buy on the checkout page POSTs to the existing `/api/checkout/start` and redirects to Stripe.

## Architectural decisions

1. **URL: `/checkout` (extension-less) via an explicit handler.** `mux.Handle("/", staticHandler)` already serves `index.html`, `*.css`, `*.js`, etc. from `public/`. The static `FileServer` would naturally serve `/checkout.html` for free, but `/checkout` reads better and is what humans will type. Add one line in `server/internal/httpserver/router.go`:
   ```go
   mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
       http.ServeFileFS(w, r, publicFS, "checkout.html")
   })
   ```
   Placed before the `mux.Handle("/", staticHandler)` fall-through. One-line rationale: clean URL with negligible code cost; `http.ServeFileFS` (Go 1.22+) handles MIME + caching headers identically to `FileServer`.

2. **Design preview = client-side SVG re-render from query params.** The SVG template (the seven `<tspan class="main-text">` blocks, the embedded Helvetica-Black `@font-face`, the `<style>` block) is duplicated into `checkout.html`. The duplication is unavoidable because the font-data URL is inline and tied to the SVG's local `<style>` — extracting it would balloon scope. The shared resize logic (`resizeSVG`, the `?text`/`?middletext` restoration in `init()`) handles the rest.

3. **One `script.js`, branched by DOM presence.** Matches the existing convention (`if (buyShirt) { ... }` is already in the file). Add a parallel branch for the checkout page keyed off `#buy-shirt-final` (or similar). No new file. The shared helpers (`escapeHTML`, `readVariantCatalog`, `readSelectedVariantID`, `resizeSVG`, `init`, the popstate handler) all live in one scope and are invoked or not based on which page rendered.

4. **No validation feedback on the generator.** The `maxlength="10"` attributes on both inputs cap the only realistic input-shape issue. Server-side `render.Validate` is still the source of truth on checkout. No `#buy-error` container on `/`; the rare malformed input falls through to a checkout-page error.

5. **Canceled banner stays on the generator (`/?canceled=1`).** No change to `cancel_url` in `checkout_handlers.go`. Rationale: a user who canceled probably wants to redesign, not re-pick a size with the same design — the generator is the right landing pad. `renderCanceledState` stays in `script.js` and runs when `?canceled=1` is in the URL on `/`.

6. **Thanks state stays on the generator (`/?session_id=...`).** Same logic — `success_url` is unchanged. `renderThanksState` is page-replacing, so the originating page doesn't matter.

7. **Price source = unchanged.** Server is authoritative (`STRIPE_PRICE_USD_CENTS` env override, else `printful.DefaultVariants[].RetailPrice`). The checkout page renders `$30` as static HTML — informational only. A comment in `checkout.html` documents this so a future reader doesn't try to wire up a dynamic price.

8. **Back button: works for free.** Clicking Buy on `/` calls `window.location.href = '/checkout?text=' + encodeURIComponent(text) + '&middletext=' + encodeURIComponent(middle)`. Browser history pushes the new entry. Back returns to `/` with whatever query params were already there from the input-keyup handlers, and `init()` re-reads them and restores the inputs. No new code needed.

## Concrete changes

### `server/public/index.html`

Remove:
- The `<fieldset id="size-picker">` block.
- The `<div id="buy-error" ...>` element.
- The variant catalog HTML comment + `<script id="variant-catalog" type="application/json">` block.

Keep:
- All `<head>` content (meta tags, favicon, stylesheet, `<script src="./script.js">`).
- Both `<input>` elements + their labels.
- `<div id="export">` and `<div id="buy-shirt">`.
- The full `<svg>` block — the live preview.
- The `<div id="image-view" hidden>` overlay block (the Save Image flow).

The Buy button text stays "Buy a Shirt" but its handler now navigates rather than fetches.

### `server/public/script.js`

Change `createTShirt` to a navigate-to-checkout function:
```js
function createTShirt() {
    const main = document.querySelector('#main-input').value || '';
    const middle = document.querySelector('#highlight-input').value || '';
    const params = new URLSearchParams();
    if (main) params.set('text', main);
    if (middle) params.set('middletext', middle);
    window.location.href = '/checkout' + (params.toString() ? '?' + params.toString() : '');
}
```

The whole 503/502/400 error-handling block (the post-`/api/checkout/start` branches) moves to a new function (`startCheckout` or similar) gated on the presence of `#buy-shirt-final`. `clearError`/`showError` remain — they now operate against the checkout page's `#buy-error`.

`renderThanksState`, `renderCanceledState`, the `?session_id` and `?canceled=1` branches at the top of `window.onload`, the popstate handler, `createImage`, and `init` all stay as-is.

Add at the bottom of `window.onload`, after the existing `if (buyShirt)` block:
```js
const finalBuy = document.getElementById('buy-shirt-final');
if (finalBuy) {
    finalBuy.addEventListener('click', startCheckout);
}
```

The `readVariantCatalog` and `readSelectedVariantID` functions only run on the checkout page now (where the catalog script + size picker live). They become no-ops on `/` because the DOM nodes don't exist — fine, nothing on `/` calls them.

Note on `init()` and `resizeSVG()`: both currently assume the generator's editable inputs and SVG. On `/checkout`, there are no editable inputs but the SVG is identical (with a `.main-text`/`.hollow`/`#filled-text` structure populated from the URL params). Add a small `populatePreview()` for the checkout page that just sets `.main-text`/`#filled-text` from query params and calls `resizeSVG`. Keep `init()` running on `/` only (it touches `#main-input` / `#highlight-input` unconditionally today). This avoids sprinkling nullable guards through `init()`.

### `server/public/checkout.html` (new file)

Sketch:
```html
<!DOCTYPE html>
<html lang="en">
<head>
    <title>Checkout — Thank You Bag</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width" />
    <link rel="icon" href="./favicon.ico" sizes="any">
    <link rel="stylesheet" href="./style.css" />
    <script src="./script.js"></script>
</head>
<body class="checkout-page">
    <a href="/" class="back-link">← Edit design</a>

    <div class="checkout-preview">
        <!-- Same SVG block as index.html, copied verbatim.
             The font @font-face inside the <style> means we can't extract;
             duplication is deliberate. -->
        <svg xmlns="http://www.w3.org/2000/svg"> ... </svg>
    </div>

    <div class="checkout-details">
        <div class="checkout-price">$30 USD</div>
        <div class="checkout-shipping">Free worldwide shipping</div>

        <fieldset id="size-picker" class="size-picker">
            <legend>Size</legend>
            <label><input type="radio" name="size" value="S"> S</label>
            <label><input type="radio" name="size" value="M" checked> M</label>
            <label><input type="radio" name="size" value="L"> L</label>
            <label><input type="radio" name="size" value="XL"> XL</label>
        </fieldset>

        <div id="buy-shirt-final" class="styled-button">Buy a Shirt</div>
        <div id="buy-error" class="buy-error" hidden></div>
    </div>

    <!-- Price is informational; server is the source of truth via
         STRIPE_PRICE_USD_CENTS or printful.DefaultVariants[].RetailPrice.
         Keep this synced with internal/printful/catalog.go. -->

    <!-- Variant catalog — moved from index.html. Keep in sync with
         server/internal/printful/catalog.go. -->
    <script id="variant-catalog" type="application/json">
        { "S": 0, "M": 0, "L": 0, "XL": 0 }
    </script>
</body>
</html>
```

The SVG block is copy-pasted from `index.html`. No JS inline; everything runs from `script.js` via DOM-presence branches.

### `server/public/style.css`

Add minimal layout for the new page (no rewrites of existing rules):
- `body.checkout-page` — center column, `padding`, no `overflow: hidden`.
- `.back-link` — small link styling, top-left.
- `.checkout-preview` — wraps the SVG; pin width/scale similar to existing media queries (the SVG `transform: scale(...)` rules already apply unconditionally).
- `.checkout-details` — stacks price/shipping/size/Buy with `gap`.
- `.checkout-price` — large font, red, matches the brand.
- `.checkout-shipping` — smaller, black.

Reuse: `.styled-button`, `.styled-button.is-loading`, `.size-picker` + its descendants, `.buy-error`. These are unchanged.

### `server/internal/httpserver/router.go`

Add one route before the static fall-through:
```go
mux.HandleFunc("GET /checkout", func(w http.ResponseWriter, r *http.Request) {
    http.ServeFileFS(w, r, publicFS, "checkout.html")
})
```

No other server changes.

### `server/public/embed.go`

No code change — the existing pattern `//go:embed *.html ...` already matches `checkout.html`.

## Tests

Frontend-only — no Go tests change. Manual test plan:

1. **Generator clean state.** Visit `/`. No size picker, no `#buy-error`, no variant catalog `<script>`. Inputs work; SVG preview updates as you type.
2. **Navigate to checkout.** Type `HELLO` in main, `WORLD` in middle. Click Buy. URL becomes `/checkout?text=HELLO&middletext=WORLD`. Page shows the same SVG preview with HELLO/WORLD, $30 price, free worldwide shipping copy, size radios (M default), Buy button, no error visible.
3. **Final buy → Stripe.** With variant catalog populated (real ids), click Buy on checkout. Network tab shows `POST /api/checkout/start` with body `{text:"HELLO", middletext:"WORLD", variant_id:<M's id>}`. On 200 the browser redirects to Stripe.
4. **Back button restores design.** From `/checkout?text=HELLO&middletext=WORLD`, click browser Back. Lands on `/?text=HELLO&middletext=WORLD` — inputs prefilled, SVG preview matches.
5. **Stripe cancel → generator banner.** Trigger a Stripe Checkout cancel. Browser lands on `/?canceled=1` — generator with the canceled banner above the inputs, design preserved.
6. **503 paths on checkout.** With `PRINTFUL_TOKEN` unset (or catalog placeholder zeros), submit Buy on checkout. `#buy-error` renders "Shop is not configured yet…" (or the catalog-incomplete variant). Error clears on the next click attempt.
7. **Save Image still works.** From `/`, click "Save Image". The image-view overlay still appears; back button still hides it (popstate handler unchanged).
8. **Direct visit `/checkout` with no params.** Defaults: preview shows `THANK YOU` everywhere (since neither query param is set, the SVG's hardcoded default `THANK YOU` content stays). Size = M. Buy still works (server still validates).

## Acceptance criteria

- `/` has no `#size-picker`, no `#buy-error`, no `#variant-catalog` script tag.
- Clicking `#buy-shirt` on `/` performs `window.location.href = '/checkout?text=...&middletext=...'` (full navigation, browser history pushed).
- `/checkout` is served by the server (200 with the new HTML).
- `/checkout` renders the same SVG preview as `/` for the given text/middletext query params.
- `/checkout` shows `$30 USD`, "Free worldwide shipping", a working S/M/L/XL radio (default M), a Buy button, and a hidden error container.
- The Buy button on `/checkout` POSTs `{text, middletext, variant_id}` to `/api/checkout/start` and redirects to Stripe's URL on success.
- All existing 400/502/503 error codes still render inline on the checkout page's `#buy-error`.
- Browser back from `/checkout` returns to `/` with inputs restored.
- `/?canceled=1` still shows the canceled banner; `/?session_id=...` still shows the thanks state.
- No Go test changes required; `go build ./...` and `go test ./...` pass.

## Out of scope

- Thanks state and canceled banner placement (kept on `/`).
- Price authority (server-side, unchanged).
- Variant-catalog content (still placeholder zeros until the human fills them in per `internal/printful/catalog.go`).
- The SVG template itself (reused, not modified).
- The Save Image flow.
- Mobile-specific layout polish for the new checkout page beyond what the existing media queries provide.
