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

3. Start the server:

   ```
   go run ./cmd/server
   ```

   Listens on `:8080` by default. Override the port with the `PORT` env var. Override the data directory (default `./data/files`) with `DATA_DIR`. See [server/cmd/server/main.go](server/cmd/server/main.go).

4. Verify:

   - `curl http://localhost:8080/healthz` returns `ok`.
   - `http://localhost:8080/` serves the embedded static site.
   - `curl -X POST http://localhost:8080/api/render -d '{"text":"FOO","middletext":"BAR"}'` returns `{"file_id","url"}` and writes `data/files/{hash}.png`. Fetch the PNG at `GET /api/files/{hash}.png`.

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

- **HTTP surface.** Exactly four routes wired in [server/internal/httpserver/router.go](server/internal/httpserver/router.go):

  - `GET /healthz` — liveness check.
  - `POST /api/render` — validate inputs, hash, render, return `{file_id, url}`.
  - `GET|HEAD /api/files/{hash}.png` — stream the saved PNG with the immutable cache header.
  - `/` — static fall-through serving the embedded site.

  Validation, JSON shaping, body-size limits, and cache headers live in [server/internal/httpserver/handlers.go](server/internal/httpserver/handlers.go).

- **What's deferred.** Printful API integration (mockups, sync products, orders) and the deploy + DNS cutover from GitHub Pages to the Go server.

## Repo layout

The GitHub Pages site sits at the repo root:

- [index.html](index.html), [style.css](style.css), [script.js](script.js) — the page and its preview/canvas logic.
- [Helvetica-Black.woff](Helvetica-Black.woff), [Helvetica-Black.woff2](Helvetica-Black.woff2) — the display font, shared verbatim with the server.
- [splash.png](splash.png), [favicon.ico](favicon.ico), [CNAME](CNAME) — splash image, favicon, and the GitHub Pages custom-domain pointer.

The Go server lives under [server/](server/):

- [server/cmd/server/main.go](server/cmd/server/main.go) — entry point, env wiring, signal handling.
- [server/internal/render/](server/internal/render/) — input validation, hashing, SVG template expansion, resvg rasterisation.
- [server/internal/httpserver/](server/internal/httpserver/) — router, handlers, embedded-static FS.
- [server/internal/files/](server/internal/files/) — content-addressed PNG store with singleflight dedup and atomic writes.
- [server/tools/copy-static.sh](server/tools/copy-static.sh) — refresh the embedded static FS from the repo root.
- `server/data/files/` — runtime PNG output (gitignored).

The [.lattice/](.lattice/) directory holds plans, tasks, and session events for ongoing work.

## Tasks tracked in Lattice

This project uses [Lattice](.lattice/) for file-based, event-sourced task tracking. Run `lattice list` (or read [.lattice/plans/](.lattice/plans/)) for current state — see [CLAUDE.md](CLAUDE.md) for the workflow.
