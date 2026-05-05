# TYB-9: Implement Printful API integration to create t-shirts from saved designs

> **Companion to TYB-6 (planning) and TYB-5 (server scaffold).** This plan extends both. Read [task_01KQV5WQVAVKRHMKT5B5VA3BSH.md](task_01KQV5WQVAVKRHMKT5B5VA3BSH.md) first — the file-URL caching, hash strategy, validation rules, failure-mode taxonomy, and HTTP conventions all live there. This plan does not re-derive them; it cites by section number.

## 1. Decision summary

When the user clicks **Buy Shirt** (renamed to **Create T-Shirt** for honesty), the browser POSTs `{text, middletext}` to a single new server endpoint, `POST /api/printful/products`. The server renders the print PNG (reusing the path TYB-6 shipped), then makes **two independent Printful calls in parallel goroutines**: a v2 mockup-task POST (preview of the design on the tee) and a v1 sync-product POST (the orderable product). Both calls receive the same public file URL. The server returns `{file_id, file_url, sync_product_id, external_id, mockup_task_id, mockup_status_url}` in one response. Mockup polling is a separate `GET /api/printful/mockup/{task_id}` round-trip.

**Open assumptions baked in (each surfaced as a `needs_human` in §13):** Printful token will be supplied via env at deploy time; default product is Bella+Canvas 3001 (catalog id `71`); default variant set is white in S/M/L/XL with IDs to be confirmed at runtime by the implementation agent (Printful docs do not enumerate them); local end-to-end testing requires a public tunnel (deferred to a separate ops task); the **Buy Shirt** button is renamed in this task.

**Locked scope** (do not re-evaluate): mockup task + sync product creation. Out: order placement, payments, multi-product, variant picker UI.

## 2. End-to-end flow

```
Browser                         Server (Go)                      Printful
   |                                 |                              |
   | POST /api/printful/products     |                              |
   | {text, middletext}              |                              |
   |-------------------------------->|                              |
   |                                 | 1. validate (TYB-6 §7)       |
   |                                 | 2. hash; SaveDedup -> file   |
   |                                 |    /api/files/{hash}.png     |
   |                                 | 3. external_id =             |
   |                                 |    "tyb-" + hash[:12]        |
   |                                 |                              |
   |                                 | 4. fan out (errgroup):       |
   |                                 |    g1: POST /v2/mockup-tasks |
   |                                 |        with file URL        -|----> Printful
   |                                 |                              |      (preview render)
   |                                 |    g2: GET  /store/products  |
   |                                 |        /@{external_id}      -|----> Printful
   |                                 |        if 200: reuse it      |      (idempotency check)
   |                                 |        if 404: POST          |
   |                                 |             /store/products -|----> Printful
   |                                 |                              |      (create sync product)
   |                                 |                              |
   |                                 | 5. wait for both, merge      |
   |   200 OK                        |                              |
   |   {file_id, file_url,           |                              |
   |    sync_product_id, external_id,|                              |
   |    mockup_task_id,              |                              |
   |    mockup_status_url}           |                              |
   |<--------------------------------|                              |
   |                                 |                              |
   | (UI shows status; polls         |                              |
   |  mockup_status_url every 1.5s)  |                              |
   | GET /api/printful/mockup/{id}   |                              |
   |-------------------------------->|                              |
   |                                 | passthrough                 -|----> Printful
   |   {status: "completed",         |                              |
   |    mockups: [{...url}]}         |                              |
   |<--------------------------------|                              |
```

**Why parallel goroutines server-side:** the two Printful calls are independent (mockup task does not need the sync product, and vice versa) but both depend on the same PNG. The server has the file URL right after `SaveDedup`; fanning out cuts latency roughly in half versus serial. `errgroup.Group` from `golang.org/x/sync/errgroup` is the right primitive — both errors surface, and a partial failure (e.g. mockup succeeded, sync product failed) returns 502 with a body listing what worked. The user can retry; the sync product is idempotent on `external_id` so retry is safe.

**Why not client-driven dance:** two extra round-trips, browser sees the bearer token via headers if we're not careful, and there's no UX win from the client knowing the intermediate state. Keep the orchestration server-side.

## 3. The Printful HTTP client

Lives at `server/internal/printful/`. Files:

- `client.go` — `Client` struct, constructor, do/request helper, auth headers, retry/backoff.
- `mockup.go` — `CreateMockupTask`, `GetMockupTask`.
- `products.go` — `CreateSyncProduct`, `GetSyncProductByExternalID`.
- `catalog.go` — default product/variant config; constants for product_id `71` and a `DefaultVariants` slice (see §6).
- `errors.go` — typed errors (`ErrUnauthorized`, `ErrRateLimited`, `ErrNotFound`, `APIError`).
- `client_test.go`, `mockup_test.go`, `products_test.go` — `httptest.NewServer` table-driven tests.

### Client construction

```go
type Client struct {
    httpClient *http.Client
    baseURL    string  // default "https://api.printful.com"; overridable for tests
    token      string  // never logged
    storeID    string  // optional; sets X-PF-Store-Id when non-empty
    logger     *log.Logger
}

type Config struct {
    Token, StoreID, BaseURL string
    Timeout                  time.Duration  // default 30s
    Logger                   *log.Logger
}

func New(cfg Config) (*Client, error)  // errors if Token == ""
```

The constructor returning an error means `main.go` can detect missing config at boot and pass `nil` through to `Handlers`; the route layer checks `nil` and 503s. Don't fail server startup over a missing Printful token — the render path still works without it and is useful in dev.

### Auth + headers

Every request: `Authorization: Bearer {token}`, `Content-Type: application/json`, `Accept: application/json`. If `storeID != ""`, also `X-PF-Store-Id: {store_id}`. Set a `User-Agent` like `thankyou-server/0.1` so Printful's logs are debuggable.

### Endpoints to wrap

| Method | Path | Notes |
|---|---|---|
| POST | `/v2/mockup-tasks` | Newer API; the v1 mockup-tasks endpoint also exists but v2 is what's documented and recommended. Body is `{format, products:[{source:"catalog", catalog_product_id, catalog_variant_ids:[...], placements:[{placement, technique, layers:[{type:"file", url}]}]}]}`. Response: `{id, status, catalog_variant_mockups: [], failure_reasons: []}`. |
| GET | `/v2/mockup-tasks?id={id}` | Polling. Response same shape; `status` transitions `pending` → `completed` (or `failed`); `catalog_variant_mockups[].mockups[].mockup_url` populated when complete. |
| GET | `/store/products/@{external_id}` | Idempotency check. 404 means not present. v1 path; v2 has no sync-product endpoint yet ("Product management ... is not available in version 2"). |
| POST | `/store/products` | v1. Body `{sync_product:{external_id, name, thumbnail}, sync_variants:[{external_id, variant_id, retail_price, files:[{type, url}]}]}`. Response `{result:{id, external_id, sync_variants:[...]}}`. |

The v1/v2 split is a real footgun: store-product calls go to `https://api.printful.com/store/products` (no `/v2`); mockup calls go to `https://api.printful.com/v2/mockup-tasks`. Encode the prefix in the per-method helper, not on the client. TYB-6 plan §3 documents this; preserved here.

### Go types (sketches)

```go
// mockup.go
type CreateMockupTaskRequest struct {
    Format   string                  `json:"format"` // "png"
    Products []MockupProduct         `json:"products"`
}
type MockupProduct struct {
    Source            string       `json:"source"` // "catalog"
    CatalogProductID  int          `json:"catalog_product_id"`
    CatalogVariantIDs []int        `json:"catalog_variant_ids"`
    Placements        []Placement  `json:"placements"`
}
type Placement struct {
    Placement string  `json:"placement"`  // "front"
    Technique string  `json:"technique"`  // "dtg"
    Layers    []Layer `json:"layers"`
}
type Layer struct {
    Type string `json:"type"`  // "file"
    URL  string `json:"url"`
}

type CreateMockupTaskResponse struct {
    ID                     int64                  `json:"id"`
    Status                 string                 `json:"status"`
    CatalogVariantMockups  []VariantMockup        `json:"catalog_variant_mockups"`
    FailureReasons         []string               `json:"failure_reasons"`
}

// products.go
type CreateSyncProductRequest struct {
    SyncProduct  SyncProduct   `json:"sync_product"`
    SyncVariants []SyncVariant `json:"sync_variants"`
}
type SyncProduct struct {
    ExternalID string `json:"external_id"`
    Name       string `json:"name"`
    Thumbnail  string `json:"thumbnail,omitempty"`  // file URL
}
type SyncVariant struct {
    ExternalID   string       `json:"external_id"`
    VariantID    int          `json:"variant_id"`
    RetailPrice  string       `json:"retail_price"`
    Files        []SyncFile   `json:"files"`
}
type SyncFile struct {
    Type string `json:"type"`  // "default" or "front"
    URL  string `json:"url"`
}

type CreateSyncProductResponse struct {
    Result struct {
        ID           int64         `json:"id"`
        ExternalID   string        `json:"external_id"`
        SyncVariants []SyncVariant `json:"sync_variants"`
    } `json:"result"`
}
```

### Error handling

The `do()` helper centralises the response handling:

- 2xx — JSON-decode into the typed response.
- 4xx — decode `{code, result, error:{message}}`, return typed `APIError{StatusCode, Message, RawBody}`. 401 → `ErrUnauthorized`. 404 → `ErrNotFound`. 422 → validation, surface message.
- 429 — read `Retry-After` header (seconds), sleep, retry once. If still 429, return `ErrRateLimited` with the duration. One retry only — orchestration above (§4) decides whether to surface or queue.
- 5xx — retry once with 500ms backoff, then surface as `APIError`.
- network/timeout — surface as-is, no retry (the caller chose the timeout for a reason).

For TYB-9 V1, single-user prototype hits 429 effectively never. The retry-once-on-429 path is documented for correctness; don't add a token-bucket scheduler.

### Logging

Structured: `request_id, endpoint, status, latency_ms, retry_count, err?`. **Never log the bearer token; never log full request bodies** (file URLs are fine; nested structures aren't worth the disk). Pull `request_id` off the inbound HTTP request via a context key set by middleware (TYB-6 plan §8 logging convention).

### Testing

- One test file per endpoint module.
- `httptest.NewServer` returning hand-crafted JSON responses for happy/4xx/5xx/429 paths.
- Table-driven for error rows: `{name, statusCode, body, headers, expectErr}`.
- Hand-craft golden response bodies in `testdata/`.
- Crucially: a `Test429RetriesOnceThenSurfaces` and a `TestNoTokenIsConstructorError`.

## 4. New server routes

| Method | Path | Notes |
|---|---|---|
| POST | `/api/printful/products` | Body `{text, middletext, background?}` (same shape as `/api/render`). Validates, renders, saves, then runs the parallel mockup+sync-product fanout. Returns `{file_id, file_url, sync_product_id, external_id, mockup_task_id, mockup_status_url}`. 503 if Printful unconfigured; 502 on partial Printful failure (with a `partial: {mockup_ok, sync_product_ok}` field for the client to react). |
| GET | `/api/printful/mockup/{task_id}` | Path-param parsing same shape as `/api/files/{hash}.png` — no router upgrade needed. Validate task_id is `^[0-9]{1,20}$` to avoid passing arbitrary strings to the upstream. Pass-through proxy: server holds the bearer token, browser does not. Returns whatever Printful returns (typed and re-encoded to keep the response shape stable). 503 if Printful unconfigured. |

**Optional (recommend yes):** `POST /api/printful/mockup` standalone — body `{file_id}` or `{text, middletext}`; renders if needed, kicks off mockup task only. Useful for dev (the user wants a preview without committing to a sync product). Cheap to add; reuses the mockup-only path in `/api/printful/products`. If yes, the response is `{task_id, status_url, file_url}`.

**503 body shape on missing config:**
```json
{"error":"printful_unconfigured","message":"server is missing PRINTFUL_TOKEN; the design was rendered and saved but Printful integration is offline","file_id":"...","file_url":"..."}
```
Returning the file id + url even on the 503 path means the client UI can still degrade gracefully (show the saved design) when running in unconfigured dev.

**Status code conventions** (consistent with TYB-6 §7 and existing `/api/render`):
- 200 — full success.
- 400 — validation (text too long, etc.).
- 404 — file or task id not found (mockup status endpoint only).
- 502 — Printful upstream failed; body explains which call(s).
- 503 — server not configured for Printful (token missing).
- 500 — internal error (render failure, disk full, etc.).

## 5. Wiring "Buy Shirt"

Decision: **rename to "Create T-Shirt"**. The current button text is dishonest now that no payment happens, and was even more dishonest as a render-only debug. New label is clearer and matches what `/api/printful/products` does.

- Update `index.html` `#buy-shirt` text to "Create T-Shirt". Keep the id (lots of CSS already keyed on it; no churn).
- Update `script.js` `requestServerRender` (rename to `createTShirt`) to POST `/api/printful/products` instead of `/api/render`.
- The "view print file" debug behavior currently triggered by the same button moves to a new dev-only `?dev=1` query handler that hits `/api/render` directly, OR is removed (it was scaffolding). Recommend removing — anyone who needs the raw PNG can curl `/api/render`.
- On 200: replace body content with two links — one to the file URL (the print PNG, kept for debug), one to the (eventual) Printful dashboard URL of the sync product (`https://www.printful.com/dashboard/sync/products/{sync_product_id}` — confirm format with the implementation agent). Show "Generating mockup..." with a spinner; poll `mockup_status_url` every 1.5s until `status: "completed"` (or 5 minute timeout — Printful is usually < 30s); then show the mockup image.
- On 503: show "Server not configured for Printful — your design was saved at {file_url}". Don't crash the UX in the unconfigured case.
- On 502: "Created T-Shirt partially — {what worked} succeeded, {what failed}; please retry."
- On 4xx/5xx: existing handling.

This keeps one round-trip from the browser's perspective; the mockup polling is separate and can be cancelled by the user navigating away.

## 6. Variant selection

V1: hardcoded defaults in `server/internal/printful/catalog.go`:

```go
const BellaCanvas3001ProductID = 71

// DefaultVariants is the V1 set: Bella+Canvas 3001 unisex tee, white,
// sizes S/M/L/XL. Variant IDs are NOT reliably listable from Printful's
// public docs. The implementation agent must confirm them by either:
//   - calling GET /products/71 against a live Printful account, OR
//   - asking the human (see needs_human #3).
// Until confirmed, these are placeholders that will fail validation at
// the first POST /store/products call.
var DefaultVariants = []DefaultVariant{
    {Size: "S", VariantID: 0 /* TODO: confirm */, RetailPrice: "25.00"},
    {Size: "M", VariantID: 0 /* TODO: confirm */, RetailPrice: "25.00"},
    {Size: "L", VariantID: 0 /* TODO: confirm */, RetailPrice: "25.00"},
    {Size: "XL", VariantID: 0 /* TODO: confirm */, RetailPrice: "25.00"},
}
```

The catalog file's job is to be the one place a human edits when the variant IDs are confirmed. **Do not use `panic` or `init`-time validation** — the server should still boot with `0` placeholders so other endpoints work; the Printful endpoints will surface a clear 502 when the placeholder hits the wire.

UI variant picker: out of scope V1 (the orchestrator confirmed). Server should accept variant IDs in the request body if provided (`{text, middletext, variant_ids?: [int]}`) and fall back to `DefaultVariants` otherwise — this lets a curl test pin specific IDs without code changes, costs ~10 lines.

Retail price `25.00` is a guess; flag in `needs_human` #3.

## 7. Caching and idempotency

Reuses TYB-6 §6 file caching unchanged. Added for sync products:

- **External ID derivation:** `external_id = "tyb-" + file_id[:12]`. Deterministic from the design alone (does not depend on variant set — see open question below). 12 hex chars = 48 bits of entropy, collision-resistant for any plausible single-user catalog. Prefix `tyb-` makes it greppable in Printful's dashboard.
- **Idempotency flow:** before posting to `/store/products`, do `GET /store/products/@{external_id}`. Three cases:
  1. **200** — sync product already exists. Decode response, return its id+external_id without re-creating. (Cheapest; happens on every duplicate click after the first.)
  2. **404** — does not exist. POST `/store/products` to create.
  3. **other** — surface as 502.
- **Race:** two concurrent identical requests both see 404, both POST. Printful's response on the second POST will be a 4xx; the client treats that as success and re-fetches via the GET path. If Printful tolerates duplicate `external_id`, the second creates a duplicate (unlikely but undocumented). Mitigation: a `singleflight` group keyed on `external_id` inside the products handler — same primitive as `files.Store`. Cheap; eliminates the race deterministically.
- **Variant set caveat:** if the user clicks "Create T-Shirt" once with default variants, then again with a custom variant set in a future task, the second request sees the existing sync product and returns it unchanged — the new variants would be ignored. **Open question for the V2 picker task:** include variant set in the external_id hash, or always update sync_variants on revisit? Out of scope here; flag in `needs_human` #4.

## 8. Public URL prerequisite

The Printful API GETs the print file from the URL we hand it. This means:

- The `file_url` on the wire to Printful must be **publicly reachable**, not `http://localhost:8080/...`.
- New env var `PUBLIC_BASE_URL` (default empty in dev). The handler builds `file_url = PUBLIC_BASE_URL + "/api/files/{hash}.png"` for the URL passed to Printful, and `/api/files/{hash}.png` (relative) for the URL returned to the browser. Two different URLs for two different consumers.
- When `PUBLIC_BASE_URL == ""`, the server logs a warning at boot ("Printful integration enabled but PUBLIC_BASE_URL unset; mockup/sync POSTs will fail with file fetch errors") and still constructs an absolute URL using the `Host` header of the inbound request as a best-effort fallback. The fallback works for ngrok/cloudflared local tunnels that forward the host header correctly.
- Local dev tunneling (ngrok / cloudflared / tailscale funnel) is **out of scope this task**. Document in README under "Printful integration": "to test against the real Printful API locally, expose `:8080` via ngrok or similar and set `PUBLIC_BASE_URL=https://your-tunnel.ngrok.app`."
- Production: a real deploy gives a stable HTTPS URL. Out of scope; deferred from TYB-5.

## 9. Documentation deliverables

Per CLAUDE.md `### Documenting Shipped Work`, this task must update [README.md](../../README.md) before being marked done:

- New "Printful integration" subsection in the run section explaining env vars (`PRINTFUL_TOKEN` required for the integration, `PRINTFUL_STORE_ID` optional for account-level tokens, `PUBLIC_BASE_URL` required for end-to-end testing) and the public-URL prerequisite (link to ngrok or equivalent).
- Add the three new endpoints to the verification list (`POST /api/printful/products`, `GET /api/printful/mockup/{task_id}`, `POST /api/printful/mockup`) with sample curl commands and expected 503 behavior when `PRINTFUL_TOKEN` is unset.
- Architecture section: a new bullet on the print-file flow ("server hosts the PNG; Printful GETs it; Printful needs a public URL; we pass `PUBLIC_BASE_URL + /api/files/{hash}.png`"). Also a bullet on idempotency via `external_id`.
- "What's deferred" bullet updated to remove "Printful API integration" and add "order placement + payments, multi-product, deploy + DNS cutover."

## 10. Failure modes (deltas only)

Everything in TYB-6 §8 still applies. Sync-product-specific additions:

| Failure | Detection | Response |
|---|---|---|
| Invalid variant ID (placeholder still 0, or wrong) | Printful 422 | 502, body lists which variant ID failed; agent updates `catalog.go` |
| File URL 404 on Printful's side mid-fetch | Printful's task fails with `failure_reasons` populated | mockup task `status:"failed"`; client surfaces "mockup unavailable, design was saved"; sync_product still succeeds |
| User re-creates after deleting their store product (external_id collision) | GET `/store/products/@{external_id}` returns 404 → POST `/store/products` succeeds with same external_id | works correctly; new sync product gets the same external_id, idempotency is per-store |
| `PRINTFUL_TOKEN` revoked at runtime | first call returns 401 | 502 with body explaining; admin must rotate token |
| Mockup task timeout (Printful processing > 5 min) | client polling exceeds budget | client surfaces "mockup timed out, design saved"; server doesn't fail the original request |
| Partial parallel failure (mockup OK, sync product fail or vice-versa) | `errgroup.Wait` returns err, but one goroutine succeeded | 502 with `partial:{mockup_ok:bool, sync_product_ok:bool, mockup_task_id?, sync_product_id?}`; client retries the failed half |
| `PUBLIC_BASE_URL` unset and inbound `Host` is `localhost` | the constructed file URL is `http://localhost:8080/...` which Printful cannot fetch | request goes through, Printful's task fails async with file-fetch error; surfaced via mockup polling |

## 11. Testing strategy

| Layer | What | How |
|---|---|---|
| Unit | Printful client: each endpoint, each error path | `httptest.NewServer` with hand-crafted JSON; table-driven; no real network. |
| Unit | 429 retry-then-surface | server returns 429 then 200; assert one retry, request succeeds. Server returns 429 twice; assert `ErrRateLimited`. |
| Unit | external_id derivation determinism | same file_id → same external_id, across processes. |
| Unit | Handler 503 on missing config | construct `Handlers` with `Printful: nil`; POST → 503 with documented body. |
| Integration | full `/api/printful/products` flow | spin up the server with a stub Printful upstream (a `httptest.Server` mounted as the client's `BaseURL`); POST a valid request; assert 200 with the merged response shape; assert sync product was created (i.e., the upstream stub recorded a POST). |
| Integration | idempotency | POST twice; assert second call hit GET-by-external-id and skipped POST. |
| Integration | partial failure | upstream stub fails the sync-product POST; assert 502 with `partial:{mockup_ok:true, sync_product_ok:false, mockup_task_id:...}`. |
| Manual / env-gated | real Printful smoke test | `t.Skip` unless `PRINTFUL_TOKEN` env var is set; run from local with a tunnel; documented as the only test that touches real Printful. **Not run in CI.** |

Tests live alongside the code: `server/internal/printful/*_test.go`, `server/internal/httpserver/printful_handlers_test.go`.

## 12. Acceptance criteria

1. `server/internal/printful/client.go` exists with `New(Config) (*Client, error)` returning an error when `Token == ""`.
2. Each of `CreateMockupTask`, `GetMockupTask`, `CreateSyncProduct`, `GetSyncProductByExternalID` has table-driven unit tests covering happy path, 401, 404, 422, 429-then-success, 429-twice, 5xx, and network-timeout rows.
3. `POST /api/printful/products` with `{text:"FOO",middletext:"BAR"}` against a stubbed upstream returns 200 with `{file_id, file_url, sync_product_id, external_id, mockup_task_id, mockup_status_url}` and produces exactly one mockup-task POST and one sync-product POST.
4. Same request a second time produces zero new sync-product POSTs and one new mockup-task POST (mockup tasks are not idempotent in V1; future improvement).
5. `POST /api/printful/products` without `PRINTFUL_TOKEN` set returns 503 with a body that includes `file_id` and `file_url` so the client can degrade gracefully.
6. `GET /api/printful/mockup/{task_id}` proxies to upstream with the bearer header attached server-side; 401 from upstream → 502 (not 401, since the client isn't the one missing creds).
7. The `#buy-shirt` button label says "Create T-Shirt" in `index.html`; clicking it POSTs `/api/printful/products`; success surfaces `sync_product_id` and the (polled) mockup URL.
8. README updated per §9: env vars listed, endpoints verified, architecture bullet on the file-URL flow, public-URL prerequisite documented, "what's deferred" pruned.
9. `go test ./...` from `server/` passes with no real network calls; the env-gated smoke test is documented in a comment block at the top of its file.
10. `PUBLIC_BASE_URL=https://example.com go run ./cmd/server` serves; `file_url` returned to upstream Printful calls starts with `https://example.com/api/files/`.

## 13. Open questions for human (`needs_human`)

1. **Printful API token.** Do you have a private/account token? Sandbox or prod? Account-level (then we need `PRINTFUL_STORE_ID`) or store-level? Without one, all integration tests are stubs and the server runs in 503 mode for the new routes.
2. **Public-URL prerequisite for end-to-end testing.** Tunneling for local dev (ngrok/cloudflared) vs deploying first? Recommend ngrok for one-shot manual smoke, deploy as a separate task.
3. **Default variant set.** Confirm Bella+Canvas 3001 (catalog id `71`), white, S/M/L/XL. Variant IDs need to be filled into `catalog.go` — the implementation agent should call `GET /products/71` against your token to enumerate them and pick the four white-size IDs. Also: what retail price (currently placeholder `25.00`)?
4. **Variant-set-in-external_id.** Currently the external_id derives from the design only. If a user later picks a different variant set for the same design, we'd silently reuse the existing sync product. Acceptable for V1? Or hash variants in too? Recommend "design-only" for V1; revisit when the picker UI lands.
5. **Should the server expose a variant-id override in the request body?** Recommend yes, ~10 lines, lets curl tests pin variants without redeploying. UI can wait.
6. **"Buy Shirt" → "Create T-Shirt" rename.** UX call. Recommend the rename — current label implies a purchase that doesn't happen.
7. **Mockup task idempotency.** Printful mockup tasks are not idempotent on input; we'd create a new task on each click. For V1 this is fine (mockups are cheap and Printful caches by file URL anyway). Worth a follow-up to skip the call when the most recent mockup for this `file_id` is fresh? Probably not until usage shows the cost.
8. **Standalone `POST /api/printful/mockup` route.** Recommend yes; cheap to add; useful for dev. Confirm.

## Critical files for implementation

- /Users/forrest/Code/thankyou/server/internal/printful/client.go (new — top-level client + auth + retry)
- /Users/forrest/Code/thankyou/server/internal/printful/catalog.go (new — Bella+Canvas 3001 product/variant defaults; the human-edited config file)
- /Users/forrest/Code/thankyou/server/internal/httpserver/printful_handlers.go (new — `/api/printful/products`, `/api/printful/mockup/{id}`, optionally `/api/printful/mockup`; orchestrates the parallel fanout via `errgroup`)
- /Users/forrest/Code/thankyou/server/cmd/server/main.go (extend — read `PRINTFUL_TOKEN`/`PRINTFUL_STORE_ID`/`PUBLIC_BASE_URL`, construct `printful.Client`, pass to `Handlers`)
- /Users/forrest/Code/thankyou/script.js (extend — rename `requestServerRender` → `createTShirt`, point at `/api/printful/products`, add mockup polling)
- /Users/forrest/Code/thankyou/README.md (extend — env vars, endpoints, architecture bullets, public-URL prerequisite)
