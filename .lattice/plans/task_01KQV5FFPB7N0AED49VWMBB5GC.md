# TYB-5: set up a server

> **Scope update 2026-05-05:** TYB-6 (which blocks this task) narrowed the Printful integration out of the first cut. Use **TYB-6's HTTP surface** as authoritative — the Printful endpoints listed in this plan (`/api/printful/mockup`, `/api/printful/mockup/{task_id}`, `/api/printful/products`) are deferred to a follow-up task. TYB-5 implementation should ship: static file serving, `POST /api/render` (saves PNG to disk, returns `{file_id, url}`), `GET /api/files/{hash}.png`, and `/healthz`. The Go/`resvg-go`/`embed` decisions in the rest of this plan still stand. See `.lattice/plans/task_01KQV5WQVAVKRHMKT5B5VA3BSH.md`.

## Decision summary

- **Language: Go.** Faster iteration, simpler deploy, the image work here is glue (load font, render an SVG template, PNG-encode, POST to Printful) — not a CPU-bound pipeline that benefits from Rust. This is a small fulfillment-glue server, not a renderer.
- **Rendering: build the SVG server-side from a Go template, then rasterise with `resvg-go`** (Rust `resvg` via wasm bindings) — produces high-DPI PNGs deterministically and uses the embedded woff2 font directly. Fallback path: shell out to `rsvg-convert` if the bindings prove flaky on the chosen deploy target. Both options keep the SVG as the source of truth, which matches the existing client.
- **Repo layout:** add `server/` Go module; `cmd/server/main.go` is the entrypoint; embed the static site (`index.html`, `style.css`, `script.js`, fonts, icons, splash) via `//go:embed`. Keep the existing root files untouched so GitHub Pages keeps working until cutover.
- **In scope this task:** static serving, `POST /api/render` returning a print-ready PNG, `POST /api/printful/mockup` returning a Printful mockup task id + result URL. Local dev via `go run ./cmd/server`. Health check.
- **Deferred:** order creation, payments, hosting/DNS cutover, auth/admin, observability, rate limiting, CI/CD.

## Language choice — Go vs Rust

Both can do this. Recommendation: **Go**.

| Axis | Go | Rust |
|---|---|---|
| Image-rendering libs for this job | `tiny-skia`/`resvg` Go bindings, `fogleman/gg`, `golang.org/x/image/font`. Good enough. | `resvg`, `tiny-skia`, `usvg` native — best-in-class. |
| Static-file serving + HTTP | `net/http` stdlib, `chi` for routing. Trivial. | `axum`/`actix` — fine, a bit more ceremony. |
| Embed static assets in binary | `//go:embed` (stdlib). | `rust-embed` or `include_dir` crate. |
| Build/iteration speed | Fast compiles, single binary. | Slow compiles, single binary. |
| Deploy ergonomics (Fly/Railway/Render) | Excellent — first-class Go buildpacks. | Excellent but slower CI builds. |
| Library risk for SVG → PNG | Slight: Go SVG renderers are weaker than Rust's `resvg`. The `resvg-go` bindings mitigate this — they wrap the same Rust crate via wasm. | None — `resvg` is best-of-breed. |

The deciding factor: this is a small fulfillment-glue server. The *image work is one function*. Go's compile-loop speed and one-file `net/http` + `embed` story will save more time than Rust's better native SVG libs will. The `resvg-go` bindings recover Rust-quality rendering inside the Go process. If the bindings are unsatisfactory on the chosen deploy target, falling back to `rsvg-convert` as a subprocess is a five-line change.

Pick Rust instead only if (a) the user already prefers Rust, or (b) future scope includes heavy on-the-fly batch rendering (e.g. generating thousands of mockups). Neither is in evidence.

## Image rendering

The current client builds an SVG inline (see [index.html](../../index.html), the SVG block in the body) with this structure, then uses Canvas `drawImage(svgBlob)` to rasterise:

- 7 `<tspan>` lines, `dy="228"`, `font-size: 20em`, `letter-spacing: -0.08em`, all `text-transform: uppercase`, `fill: red`, `stroke: red`, `stroke-width: 2px`.
- Lines 1, 2, 3, 5, 6, 7 are `.hollow` (`fill: white`, so they render as red outline only).
- Line 4 (`#filled-text`) is fully red — this is where the user's "middle" text goes by default.
- An 8th `<tspan>` is `thankyoubag.online` at `dy="150"`, `font-size: 8em`, offset `x="28%"`.
- Background is a white `<rect>`.
- Font: Helvetica Black (woff2 in repo, base64-inlined into the SVG `@font-face`).

**Server-side approach:**

1. **Templating.** Use `text/template` to produce the SVG document from `{ MainText, MiddleText }`. The template is the same shape as the inline SVG already in `index.html`, with the woff2 base64-inlined the same way (read from disk via `embed.FS`, base64-encode at server start, splice into the template).
2. **Rasterise.** `resvg-go` (wasm-backed Rust `resvg`) takes SVG bytes + font registry and outputs RGBA pixels. Encode with `image/png`. Output dimensions: match the client's `canvas.toDataURL` output. The client's height calc is `dy * 7 + 150 + PAD_AMOUNT (=100)` and width is the rendered text bbox. Server should produce a fixed-size canvas large enough for `maxlength=10` characters at 20em — compute once or hardcode (e.g. 4096×3200) and trust `resvg` to letterbox the SVG via its own viewBox/aspect logic. **Add a `viewBox` attribute** (the current SVG has none — the client measures `getBBox()` after layout). For server rendering you must commit to a viewBox; suggest `viewBox="0 0 4096 3200"` and right-pad text overflow with the existing `letter-spacing: -0.08em`.
3. **Print quality.** Printful prefers 150–300 DPI for the print area. For a t-shirt 12"×16" print area at 300 DPI that's 3600×4800 px. Render the SVG to that size by scaling `resvg`'s output buffer; SVG is resolution-independent so just pick the target pixel size when rasterising.
4. **Font.** Load `Helvetica-Black.woff2` from `embed.FS` at boot. `resvg-go` accepts a font database — register the woff2 directly. Ship the font in the binary; do not rely on system fonts.

**Options considered and rejected:**

- *Reimplementing layout in `fogleman/gg` (cairo-style 2D drawing).* Means re-deriving the cascade (line-height, letter-spacing, stroke ordering). The SVG is already correct in the browser; reusing it is less code and cannot drift from the client.
- *Headless Chromium (chromedp / Playwright).* Correct rendering for free, but a 200 MB chromium dependency for what is otherwise a 10 MB Go binary. Defer unless `resvg` produces visibly different output from the browser.
- *Native Go SVG (`oksvg`, `srwiley/oksvg`).* Weaker on text/font features. Acceptable in theory; `resvg-go` is just better.

**Sketch (illustrative — not to be implemented in this task):**

```
// pseudo-Go
type RenderRequest struct { Text, MiddleText string }

func renderPNG(req RenderRequest, w, h int) ([]byte, error) {
    svg := svgTemplate.Execute(req)        // text/template
    worker := resvg.NewWorker(fontDB)       // boot-time init, reused
    rgba  := worker.Render(svg, w, h)
    return pngEncode(rgba)
}
```

## HTTP surface

In scope:

| Method | Path | Purpose |
|---|---|---|
| GET | `/` and `/*` | Serve embedded static site (index.html, css, js, fonts, splash, favicon). |
| POST | `/api/render` | Body: `{ "text": "FOO", "middletext": "BAR" }` (both ≤10 chars, mirrors the existing `maxlength`). Response: `image/png` of the print-ready render. Optional query `?w=3600&h=4800` to override size; defaults sized for Printful t-shirt print area. |
| POST | `/api/printful/mockup` | Body: same payload + `{ "product_id": int, "variant_ids": [int] }`. Server renders PNG, hosts it at `/api/files/{hash}.png` (in-memory or tmpfs cache, TTL ~10 min), POSTs Printful `/mockups` with that public URL, returns the task id and a `result_url` field for client polling. |
| GET | `/api/files/{hash}.png` | Short-lived cached print files referenced by Printful. Hash-keyed so callers can't enumerate. |
| GET | `/api/printful/mockup/{task_id}` | Thin proxy of Printful `GET /mockups/{task_id}` — lets the browser poll without leaking the API key. |
| GET | `/healthz` | 200 OK, used by deploy target. |

Out of scope this task: order creation, cart, payments, webhook ingestion from Printful, user accounts, rate limiting beyond a basic per-IP token bucket if trivial.

Validation: enforce `len(text) <= 10` and `len(middletext) <= 10` (matches `maxlength=10` on both inputs). Strip control chars; uppercase server-side (CSS does it client-side).

## Printful integration sketch

From the Printful API docs (https://developers.printful.com/docs/):

- **Auth:** `Authorization: Bearer {private_token}`. If account-level token, also send `X-PF-Store-Id: {store_id}`. Read both from env: `PRINTFUL_TOKEN`, `PRINTFUL_STORE_ID`.
- **Mockup endpoint:** `POST https://api.printful.com/mockups` to create a task; `GET /mockups/{task_id}` to poll for the rendered mockup URL.
- **File input:** Printful expects a **public URL** to the print file — direct upload is not part of the v2 mockup flow. This is why the server needs `/api/files/{hash}.png` to publicly host the rendered PNG temporarily. The Printful side caches by URL ("If a file with the same URL already exists, it will be reused"), so deterministic hashes are useful.
- **Rate limits:** 120 req/min global, lower for mockup generator. Single-user prototype is far under this; no special handling this task.
- **Polling shape:** Printful tasks complete in seconds. The browser hits `/api/printful/mockup` (returns `task_id`), then polls `/api/printful/mockup/{task_id}` every 1–2 s until status is `completed`.

The implementation can use `net/http` directly; no Printful Go SDK is needed (none official).

## Repo layout

```
/                          # existing site (untouched until cutover)
  index.html
  style.css
  script.js
  Helvetica-Black.woff2
  splash.png, favicon.ico
  CNAME

/server/                   # new
  go.mod
  go.sum
  cmd/server/
    main.go                # wires router, embed.FS, env config
  internal/
    render/
      render.go            # SVG template + resvg-go rasterise
      template.svg         # Go template, reused from index.html SVG
    printful/
      client.go            # auth, /mockups POST, /mockups/{id} GET
    httpserver/
      router.go            # chi routes, handlers
      static.go            # //go:embed of repo root for static serving
      files.go             # /api/files/{hash}.png ephemeral cache
  static/                  # mirror of repo root for embed; populated at build time
  Dockerfile               # added later when deployment is scoped
  README.md                # how to run locally

/.lattice/                 # unchanged
```

**Embedding strategy:** prefer copying static files into `server/static/` at build time (a tiny shell script or a `go:generate` rule), then `//go:embed static/*`. Avoids `embed`'s "no parent paths" restriction. Don't symlink — `embed` doesn't follow symlinks reliably.

**Module name:** `github.com/forrestalmasi/thankyou/server` (or whatever the user prefers — see open questions).

## Deployment (deferred to a follow-up task)

Just flagging the shape so the implementation agent doesn't paint itself into a corner:

- **Recommend Fly.io** for a single-binary Go server. Free tier is generous, `flyctl launch` reads a `Dockerfile` or autogenerates one for Go, deploys behind HTTPS with a `*.fly.dev` domain.
- DNS cutover: `thankyoubag.online`'s `CNAME` file currently makes GitHub Pages authoritative. Cutover means deleting the GH Pages CNAME (or removing the apex from the registrar's GH Pages records) and pointing A/AAAA records at Fly. **Not in scope this task.**
- Cloudflare Workers was briefly considered (Rust+wasm). Rejected: image rendering pushes against the wasm size/compute limits, and it'd force the language choice toward Rust.

Acceptance for the deferred deploy task should be: server reachable on a public URL, environment variables configured, mockup roundtrip works against real Printful API.

## Local dev

```
cd server
PRINTFUL_TOKEN=xxx PRINTFUL_STORE_ID=yyy go run ./cmd/server
```

Server listens on `:8080` by default (override with `PORT`). Visiting `http://localhost:8080/` serves the existing site unchanged. `POST /api/render` is testable via `curl`.

For hot-reload, optionally `air` (`github.com/cosmtrek/air`) — but a manual `go run` rebuild is sub-second and probably fine. Don't add `air` until friction shows up.

No Docker required for local dev. The font and static files are baked into the binary at compile time.

## Acceptance criteria

1. `go run ./cmd/server` (from `server/`) starts a server on `:8080` with no required env vars (Printful endpoints may 503 without `PRINTFUL_TOKEN` — that's fine).
2. `GET /` returns `index.html` byte-identical to the file at the repo root.
3. `GET /style.css`, `/script.js`, `/Helvetica-Black.woff2`, `/splash.png`, `/favicon.ico` all return 200 with correct `Content-Type`.
4. `POST /api/render` with `{"text":"FOO","middletext":"BAR"}` returns a PNG (`Content-Type: image/png`) where:
   - lines 1–3 and 5–7 read "FOO" in red outline (hollow);
   - line 4 reads "BAR" in solid red;
   - bottom line reads "thankyoubag.online";
   - dimensions ≥ 3600×4800 px (or whatever default is settled on).
5. The rendered PNG is visually equivalent to the client-side `Save Image` export at the same inputs. (Manual diff acceptable; pixel-perfect not required.)
6. `POST /api/printful/mockup` with valid env-var creds returns `{"task_id": "...", "status_url": "/api/printful/mockup/..."}`. Without creds returns 503 with a clear error.
7. `GET /healthz` returns 200.
8. Binary builds for `linux/amd64` and `darwin/arm64` via `go build`.

## Open questions for human (`needs_human`)

These do **not** block the planning task moving to `planned`, and the implementation can proceed using stubs/env-vars:

1. **Printful product.** T-shirt? tote? poster? mug? The render dimensions and Printful `product_id`/`variant_ids` depend on this. **Stub recommendation:** start with the Bella+Canvas 3001 unisex tee (a common Printful default; product id used as placeholder); revisit when human confirms.
2. **Printful credentials.** Does the user have a Printful account and a private token? We need `PRINTFUL_TOKEN` and (if account-level) `PRINTFUL_STORE_ID`. Implementation can be tested against the Printful sandbox with any valid token; production needs the user's.
3. **Order vs mockup scope.** This task says "make API requests to the printful api". Mockup generation only is the safe minimum and is what's planned. Order creation (charging, shipping address, fulfillment) is a multi-task follow-up. Confirm this scoping is acceptable.
4. **Domain.** Should `thankyoubag.online` point at the new server immediately (cutover), or run the server on a subdomain (e.g. `api.thankyoubag.online`) while GH Pages keeps serving the static site? Subdomain is lower-risk for a follow-up deploy task; full cutover is cleaner long-term.
5. **Stray `node_modules/`.** Contains `express`, `serve-static`, `nodemon`, `body-parser` etc. — looks like an abandoned Express experiment. There's no `package.json`. Recommend leaving it alone in this task (safer than a delete) and addressing it as a one-line cleanup task post-server. Nothing in there is worth carrying forward — Go covers all of it.
6. **Module path.** `github.com/forrestalmasi/thankyou/server`? Or a separate repo? Single-repo is simpler.
