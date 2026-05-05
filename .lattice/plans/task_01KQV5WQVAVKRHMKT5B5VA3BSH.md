# TYB-6: Plan how to process images and send them to printful to create products

> **Scope narrowed 2026-05-05.** Printful API calls are deferred to a follow-up task. This plan now covers: when the user clicks "Buy Shirt," the browser sends `{text, middletext}` to the server, the server renders the print-ready PNG and saves it to disk. Done. The Printful side will be planned and built later, on top of the artifacts this task produces.
>
> Companion to TYB-5 (server scaffold). Read `.lattice/plans/task_01KQV5FFPB7N0AED49VWMBB5GC.md` first.

## 1. Decision summary (locked)

**Server-side SVG rendering, no Printful integration this round.**

- Client POSTs `{text, middletext}` to `/api/render` when the user clicks **Buy Shirt**.
- Server expands a Go `text/template` SVG (mirrors the existing inline SVG in `index.html`, with the woff2 base64-inlined), rasterises it via `resvg-go` using the embedded `Helvetica-Black.woff2`, encodes a PNG.
- Server writes the PNG to `data/files/{sha256}.png` on disk and returns `{file_id, url}` to the client.
- Client surfaces "Saved" / "Order placed" / similar UI feedback. No Printful API call.

**Rejected, do not re-evaluate:**

- *Client dataURI upload.* Browser canvas resampling is non-deterministic across devices/zoom levels. Multi-MB upload payloads. Untrusted client bytes for a fulfillment artifact (relevant once Printful is wired up).
- *Client-side direct Printful calls.* No public-client signing flow on Printful's API. Moot for this task since Printful is deferred, but the architectural reasoning still rules this out for the future task too.

Decision logged on TYB-6 (2026-05-05) after conversation with the human.

## 2. End-to-end flow

```
Browser                                 Server (Go)
   |                                       |
   | (user types text + middletext)        |
   | (clicks "Buy Shirt")                  |
   |                                       |
   | POST /api/render                      |
   | {text:"FOO", middletext:"BAR"}        |
   |-------------------------------------->|
   |                                       | 1. validate inputs
   |                                       | 2. canon = canonicalize(text,middle)
   |                                       | 3. h = sha256(canon || tmpl_ver)
   |                                       | 4. if !exists(data/files/{h}.png):
   |                                       |      svg = template.exec(canon)
   |                                       |      png = resvg.render(svg)
   |                                       |      atomic_write(data/files/{h}.png, png)
   |   {file_id: h,                        |
   |    url: "/api/files/{h}.png"}         |
   |<--------------------------------------|
   |                                       |
   | (UI shows "Saved" or similar          |
   |  ack; optionally GET the URL          |
   |  to display the print-quality         |
   |  PNG inline)                          |
```

Out of frame (deferred to a follow-up Printful task): everything that turns the saved PNG into a Printful mockup or sync product.

## 3. The print file

Concrete decisions for the rendered PNG (these stay valid for the deferred Printful task — picking them now means the file produced by this task is ready to ship to Printful unchanged):

| Property | Value | Rationale |
|---|---|---|
| Format | **PNG** (RGBA) | Sharp-edged red text on white. JPG ringing artifacts on those edges are immediately visible. |
| Dimensions | **3600 x 4800 px** (12" x 16" at 300 DPI) | Bella+Canvas 3001 DTG print area is approximately 12" x 16"; 300 DPI is Printful's recommended target, 150 DPI the floor. Picking print-ready dimensions now avoids re-rendering later. |
| Color profile | **sRGB** (no embedded ICC) | Printful expects sRGB. |
| Background | **White rect** (matches current client SVG) | "Decal on tee" look. Open question 1. |
| Bleed/safe area | **120 px (~1%) inner margin** | Conservative; achieved via viewBox padding. |
| Filename | `{sha256}.png` | Hash-keyed; deterministic; enumeration-resistant. |

### Resolving the "no viewBox" problem

The current client SVG has *no* `viewBox`. It works in-browser because the JS calls `getBBox()` after layout. `resvg-go` cannot do post-layout measurement.

Server-side template commits to **`viewBox="0 0 4096 3200"`** and renders scaled to 3600x4800. For a 3:4 print canvas vs the 4:3.125 viewBox, the SVG renders centred vertically inside a 3600x4800 white background (uniform-scale-and-letterbox).

For long input (10 chars), the existing `letter-spacing: -0.08em` keeps text in the viewBox at `font-size: 20em`. If real-world testing shows clipping, options: (a) shrink `font-size` proportionally to input length on the server, or (b) widen `viewBox` to `4500 x 3200`. Implementation should hardcode `4096 x 3200` first, then adjust empirically — the diff against the client preview is the acceptance test.

## 4. Frontend changes

Currently [index.html](../../index.html) has an Export button (id `#export`) wired to [script.js](../../script.js) `createImage()` — that's the existing client-side rasterise-to-PNG-and-replace-document flow. This task does **not** remove it; it adds a parallel "Buy Shirt" path.

Add to the page:

- A **Buy Shirt** button (id `#buy`) styled to match existing UI.
- A click handler in [script.js](../../script.js) that:
  1. Reads `#main-input` and `#highlight-input` values (the same source `createImage()` uses).
  2. POSTs JSON `{text, middletext}` to `/api/render`.
  3. On 200, displays an ack — minimum viable: a small status line "Saved as `{file_id_short}`" with a clickable link to the returned `url`. Keeps things observable for the developer; the human can polish UX in a follow-up.
  4. On 4xx, displays the error message inline (validation failures will be the common case — empty text, too long).
  5. On 5xx, generic "Something went wrong, please try again."

Don't try to render a preview from the server's PNG inline yet — the existing client-side preview is the preview. The server's PNG is the print artifact.

Open question 4 below: should "Buy Shirt" show a confirmation modal first ("This will create your shirt design — proceed?") or just save on click?

## 5. The SVG template

Server template structure (Go `text/template`, mirrors the inline SVG in [index.html](../../index.html)):

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 4096 3200" width="3600" height="4800">
  <style>
    @font-face {
      font-family: 'Helvetica Black';
      src: url('data:application/x-font-woff;charset=utf-8;base64,{{.WoffBase64}}') format('woff');
    }
    text { fill: red; stroke: red; stroke-width: 2px; ... font-size: 20em; letter-spacing: -.08em; text-transform: uppercase; }
    .hollow { fill: white; }
    .bottom-text { font-size: 8em; }
  </style>
  <rect x="0" y="0" width="100%" height="100%" fill="{{.Background}}"/>
  <text y="10">
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="0" dy="228">{{.MiddleText}}</tspan>          <!-- filled -->
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="0" dy="228" class="hollow">{{.MainText}}</tspan>
    <tspan x="28%" dy="150" class="bottom-text">thankyoubag.online</tspan>
  </text>
</svg>
```

```go
type TemplateInput struct {
    MainText    string  // uppercased, sanitized, <= 10 chars
    MiddleText  string  // ditto; falls back to MainText if empty (matches client)
    WoffBase64  string  // computed once at server boot from embed.FS
    Background  string  // "white" by default
}
```

The woff2 is read from `embed.FS` at boot, base64-encoded once, reused per request.

XML-escape both text fields before substitution (use `template.HTMLEscapeString` or pre-sanitize).

### Client/server template sync (drift risk)

Two sources of truth for the SVG (the inline one in `index.html`, the templated one in `server/internal/render/template.svg`) will drift on the first edit. Three options:

a. **Server-canonical, client fetches template.** Server exposes `GET /api/template.svg`; client substitutes locally for preview. One source.
b. **Two copies, kept in sync manually.** A comment in both saying "must match the other"; cheap, brittle.
c. **Build-step extracts client SVG into server template.** Script reads `index.html`, pulls the `<svg>` block, writes `server/internal/render/template.svg` via `go generate`.

**Recommendation: (c).** The client SVG block in `index.html` is already canonical; extracting it is ~10 lines. (a) reimplements client-side substitution work that already exists. (b) drifts at the first edit. Fall back to (b) + a `make verify-template` diff check if (c) feels heavy in implementation.

## 6. Hash, save, retrieve

### Hash inputs

```go
func hashInputs(t TemplateInput) string {
    canonical := strings.Join([]string{
        "v1",                        // template version — bump on any template change
        normalize(t.MainText),       // NFC, uppercase, strip controls
        normalize(t.MiddleText),
        t.Background,                // "white" (default for now)
    }, "\x1f")                       // unit separator
    sum := sha256.Sum256([]byte(canonical))
    return hex.EncodeToString(sum[:])
}
```

Bumping `v1 -> v2` invalidates all caches in one move when the template changes.

### Save path

`./data/files/{hash}.png` — relative to server cwd in dev, configurable via `DATA_DIR` env var.

Atomic write: `os.WriteFile` to `{hash}.png.tmp`, then `os.Rename` to `{hash}.png`. Safe against partial reads if a concurrent request hits `GET /api/files/{hash}.png` mid-write.

Concurrent renders of the same hash: wrap in `golang.org/x/sync/singleflight` keyed on hash so only one render runs even with 20 simultaneous "Buy Shirt" clicks for the same design.

### Retrieve path

`GET /api/files/{hash}.png` serves the saved file with:

- `Content-Type: image/png`
- `Cache-Control: public, max-age=31536000, immutable` (content is hash-addressed)
- 404 if file doesn't exist (validate hash regex `^[a-f0-9]{64}$` first to avoid filesystem traversal)

### No GC

Files persist indefinitely. The whole point of saving them is so the deferred Printful task can pick them up later — GC would defeat that. A `make clean-files` target or a manual prune script can be added when storage growth becomes a real problem.

## 7. Server-side HTTP surface

| Method | Path | Status codes | Notes |
|---|---|---|---|
| POST | `/api/render` | 200, 400, 500 | Body `{text, middletext, background?}`. Returns JSON `{file_id, url}`. The PNG is *saved to disk*, not returned in the response body. |
| GET | `/api/files/{hash}.png` | 200, 404 | Hash regex `^[a-f0-9]{64}$`. `Cache-Control: public, max-age=31536000, immutable`. |
| GET | `/healthz` | 200 | Unchanged from TYB-5. |

**Removed from TYB-5's surface (deferred to follow-up Printful task):**

- `POST /api/printful/mockup`
- `GET /api/printful/mockup/{task_id}`
- `POST /api/printful/products`

This is a deliberate narrowing of TYB-5's plan. The implementation agent should follow this plan, not TYB-5's, where they conflict.

**Renamed semantics:** TYB-5's `/api/render` planned to return PNG bytes directly. TYB-6 changes that to "save and return the URL" — closer to what the user actually asked for. If returning bytes is also useful (e.g. for ad-hoc curl debugging), add `?format=bytes` as a query param. Default is save-and-return-url.

### Validation

Same as TYB-5:

- `len(MainText) ≤ 10`, `len(MiddleText) ≤ 10` (matches `maxlength=10` on the inputs).
- Strip control chars (`\x00-\x1f`, `\x7f`).
- NFC-normalise then uppercase.
- If both fields empty after trim, default `MainText` to "THANK YOU" (matches client init).

## 8. Failure modes

| Failure | Detection | Response |
|---|---|---|
| `resvg-go` render error | `worker.Render` returns err | 500, log template version + input hash (not raw SVG — it contains the woff base64 dump) |
| `resvg-go` panic | `recover()` in render goroutine | 500, restart worker; server stays up |
| Font missing/corrupt at boot | `os.ReadFile` of `Helvetica-Black.woff2` from `embed.FS` fails | refuse to start; explicit log |
| Disk full on save | `os.WriteFile` returns err | 500, log path + free space |
| Text validation failure | validator | 400 with field name and reason |
| Hash collision | sha256 — practically impossible | not handled |
| Concurrent renders, same hash | two requests, same canonicalised inputs | `singleflight` dedupes; both get the same file_id |
| Concurrent renders, different hashes | independent | each writes its own file |
| Client double-click on "Buy Shirt" | two identical POSTs in quick succession | `singleflight` dedupes server-side; client should also disable the button on click for UX |

Logging: structured JSON, one line per request (`req_id`, `endpoint`, `hash`, `latency_ms`, `error?`).

## 9. Testing strategy

| Layer | What | How |
|---|---|---|
| Unit | hash determinism | `TestHashStable`: same inputs across 100 runs and goroutines produce same hex. |
| Unit | input validation | table-driven: empty, >10 chars, control chars, unicode, mixed case. |
| Unit | template execution | `TestTemplateExpands`: substitute `FOO`/`BAR`, check output XML contains the strings in the right `<tspan>` positions. |
| Golden file | pixel render | `TestRenderGolden`: render `{text:"FOO", middletext:"BAR"}` to PNG; compare to `testdata/golden_foo_bar.png`. Hand-rolled "fraction of differing pixels < 0.1%" — `resvg` is deterministic but font hinting may shift one pixel between platforms. |
| Integration | save + serve | spin up server in test, `POST /api/render`, then `GET /api/files/{hash}.png`, assert byte-identical to render output. |
| Manual | visual regression | render server-side and client-side (`Save Image`) at the same inputs; visual diff. Acceptance is "looks the same." |

Golden file: keep one `golden_foo_bar.png` for regression; document regeneration with `go test -update`.

## 10. Acceptance criteria

1. `H = sha256(canonical("FOO","BAR"))` is identical across processes, machines, and Go versions.
2. Clicking **Buy Shirt** in the browser POSTs `/api/render` with `{text, middletext}` from the inputs.
3. `POST /api/render {"text":"FOO","middletext":"BAR"}` returns 200 with `{file_id, url}` JSON.
4. `data/files/{file_id}.png` exists on disk after the request, sized 3600x4800 px, sRGB, ≤ 5 MB.
5. The saved PNG is visually equivalent (manual diff acceptable) to the client-side `Save Image` export at the same inputs.
6. `GET /api/files/{file_id}.png` returns the PNG with `Cache-Control: public, max-age=31536000, immutable`. Same hash returns byte-identical content across requests.
7. `POST /api/render` with `text=""` AND `middletext=""` defaults to `text="THANK YOU"` (matches client behavior); response is 200.
8. `POST /api/render` with `text="ABCDEFGHIJK"` (11 chars) returns 400 with a clear field/reason.
9. Two concurrent identical POSTs produce one render call (singleflight) and both responses share the same `file_id`.
10. `resvg-go` panic during render does not kill the process — request returns 500, server stays up, next request succeeds.
11. Buy Shirt button shows ack on success, error message on 4xx, generic error on 5xx. Button is disabled while a request is in flight.

## 11. Open questions for human (`needs_human`)

1. **Buy Shirt confirmation modal.** Should clicking it show a confirmation ("This will save your design — proceed?") or save immediately? Recommend **immediate save** for now since there's no Printful charge yet — once orders are real, add the modal.
2. **Ack UI.** What does the user see after clicking Buy Shirt? Options: (a) inline status text, (b) toast, (c) modal with the file URL, (d) navigate to a confirmation page. Recommend **(a) inline status text** for the prototype — minimal styling work.
3. **Background colour.** White (matches current preview, "decal on tee" look) vs transparent (shirt color shows through hollow letter centers). Recommend **white** as default; expose `?bg=transparent` later. Will matter when Printful is wired up; harmless choice for now.
4. **`viewBox` aspect ratio.** Letterbox into 3600x4800 (preserves current line spacing exactly) vs design viewBox to be 3:4 (shifts spacing slightly to fill print area). Recommend **letterbox** to match the existing preview's geometry. Implementation agent should compare both visually.
5. **Where do saved files live in deploy?** `./data/files/` is fine for local dev. Once deployed, it's a persistent volume question (Fly.io volume, S3-compatible bucket, local disk on a VPS). Out of scope this task; flagging so the deferred deploy task addresses it.

## 12. What's been deferred

These are explicit non-goals for TYB-6 — split into a follow-up "Printful integration" task once this lands:

- All Printful API calls (`POST /v2/mockup-tasks`, `POST /store/products`, `POST /orders`).
- Mockup preview UI (showing the design on a real tee photo).
- Order placement flow (cart, shipping address, payments).
- Public hosting of `/api/files/{hash}.png` for Printful to fetch (only matters when Printful is doing the fetching).
- Catalog product/variant lookup against Printful.
- Webhook handling.
- Rate-limit handling for Printful 429s.

The follow-up task picks up where this one ends: read `data/files/{hash}.png`, POST to Printful, return mockup/product details. Nothing in this plan precludes that — the file format, dimensions, and naming were chosen to be Printful-ready.

## Critical files for implementation

- [/Users/forrest/Code/thankyou/index.html](../../index.html) — canonical SVG block (the server template is a transform of this); also where the Buy Shirt button gets added.
- [/Users/forrest/Code/thankyou/script.js](../../script.js) — current `createImage()` export logic; defines the visual contract the server must match. Add the Buy Shirt click handler here.
- [/Users/forrest/Code/thankyou/Helvetica-Black.woff2](../../Helvetica-Black.woff2) — embedded font, base64-inlined into the SVG at boot.
- [/Users/forrest/Code/thankyou/.lattice/plans/task_01KQV5FFPB7N0AED49VWMBB5GC.md](task_01KQV5FFPB7N0AED49VWMBB5GC.md) — TYB-5 plan; HTTP surface narrowed here (Printful endpoints removed).
- [/Users/forrest/Code/thankyou/style.css](../../style.css) — confirms `Helvetica Black` font stack and the red/white color choices to mirror in the SVG template; also where Buy Shirt button styles go.
